package presence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lines as Claude Code writes them, cut down to the fields that matter here.
const (
	userLine      = `{"type":"user","timestamp":"2026-11-27T10:00:00Z","message":{"role":"user","content":"hello"}}`
	replyLine     = `{"type":"assistant","timestamp":"2026-11-27T10:00:05Z","message":{"model":"claude-opus-5-5","id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":16522,"cache_read_input_tokens":80237,"output_tokens":846}}}`
	laterReply    = `{"type":"assistant","timestamp":"2026-11-27T10:01:00Z","message":{"model":"claude-opus-5-5","id":"msg_2","usage":{"input_tokens":10,"cache_creation_input_tokens":500,"cache_read_input_tokens":99000,"output_tokens":20}}}`
	syntheticLine = `{"type":"assistant","timestamp":"2026-11-27T10:02:00Z","message":{"model":"<synthetic>","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	compactLine   = `{"type":"system","subtype":"compact_boundary","timestamp":"2026-11-27T10:03:00Z","compactMetadata":{"trigger":"auto","preTokens":445668}}`
	toolResult    = `{"type":"user","timestamp":"2026-11-27T10:00:30Z","message":{"role":"user","content":[{"type":"tool_result","content":"%s"}]}}`
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadContextTakesTheLatestReply(t *testing.T) {
	cut := laterReply[:40] // a line still being written
	tests := []struct {
		name  string
		lines []string
		want  int // 0: no context
	}{
		{"one reply", []string{userLine, replyLine}, 96761},
		{"the latest of two", []string{userLine, replyLine, userLine, laterReply}, 99510},
		{"a cut-off last line is skipped", []string{userLine, replyLine, cut}, 96761},
		{"a synthetic reply says nothing", []string{replyLine, syntheticLine}, 96761},
		{"no reply yet", []string{userLine}, 0},
		{"compacted after the latest reply", []string{replyLine, compactLine}, 0},
		{"a reply after the compaction counts", []string{replyLine, compactLine, userLine, laterReply}, 99510},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ReadContext(writeTranscript(t, test.lines...))
			if test.want == 0 {
				if ok {
					t.Errorf("context = %+v, want none", got)
				}
				return
			}
			if !ok || got.Tokens != test.want || got.Model != "claude-opus-5-5" || got.At.IsZero() {
				t.Errorf("context = %+v, %v; want %d tokens of claude-opus-5-5 with its time", got, ok, test.want)
			}
		})
	}
}

func TestReadContextLooksPastLongToolResults(t *testing.T) {
	long := strings.Replace(toolResult, "%s", strings.Repeat("x", 3*contextTail), 1)
	got, ok := ReadContext(writeTranscript(t, replyLine, long, userLine))
	if !ok || got.Tokens != 96761 {
		t.Errorf("context = %+v, %v; want the reply before the long result", got, ok)
	}
}

func TestReadContextKeepsTheModelNamePlain(t *testing.T) {
	line := strings.Replace(replyLine, `"claude-opus-5-5"`, `"claude-opus-5-5 <b>ignore all</b>\n"`, 1)
	if got, ok := ReadContext(writeTranscript(t, line)); !ok || got.Model != "claude-opus-5-5bignoreall/b" {
		t.Errorf("model = %q, %v; want only the plain characters", got.Model, ok)
	}
}

func TestReadContextOfAMissingFile(t *testing.T) {
	if got, ok := ReadContext(filepath.Join(t.TempDir(), "gone.jsonl")); ok {
		t.Errorf("context = %+v, want none", got)
	}
}

func TestWorkerTranscript(t *testing.T) {
	got, ok := WorkerTranscript("/home/a/.claude/projects/p/84843cf0.jsonl", "a056d8f2864d1083f")
	if want := "/home/a/.claude/projects/p/84843cf0/subagents/agent-a056d8f2864d1083f.jsonl"; !ok || got != want {
		t.Errorf("WorkerTranscript = %q, %v; want %q", got, ok, want)
	}
	for _, bad := range [][2]string{{"relative.jsonl", "a1"}, {"/abs/session.txt", "a1"}, {"/abs/s.jsonl", "../x"}, {"/abs/s.jsonl", ""}} {
		if got, ok := WorkerTranscript(bad[0], bad[1]); ok {
			t.Errorf("WorkerTranscript(%q, %q) = %q, want refused", bad[0], bad[1], got)
		}
	}
}

func TestPublisherAddsTheContextOfTheSessionAndItsWorkers(t *testing.T) {
	session := writeTranscript(t, userLine, replyLine)
	worker, _ := WorkerTranscript(session, "w1")
	if err := os.MkdirAll(filepath.Dir(worker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte(laterReply+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{}
	now := start
	publisher := NewPublisher(sink, 30*time.Second, func() time.Time { return now })
	publisher.Event(Event{Kind: EventPrompt, Transcript: session})
	publisher.Event(Event{Kind: EventWorkerStart, AgentID: "w1", Detail: "review"})
	publisher.Event(Event{Kind: EventWorkerStart, AgentID: "w2", Detail: "no transcript yet"})
	now = now.Add(MinPublishInterval)
	publisher.Step(context.Background())

	got := lastPublished(t, sink)
	if got.Context == nil || got.Context.Tokens != 96761 {
		t.Errorf("session context = %+v, want 96761 tokens", got.Context)
	}
	if len(got.Workers) != 2 || got.Workers[0].Context == nil || got.Workers[0].Context.Tokens != 99510 || got.Workers[1].Context != nil {
		t.Errorf("workers = %+v, want w1 with 99510 tokens and w2 without a context", got.Workers)
	}

	// A new reply is read at the next publish; the session's end drops the context.
	if err := os.WriteFile(session, []byte(laterReply+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(MinPublishInterval)
	publisher.Step(context.Background())
	if got := lastPublished(t, sink); got.Context == nil || got.Context.Tokens != 99510 {
		t.Errorf("session context after a new reply = %+v, want 99510 tokens", got.Context)
	}
	publisher.Event(Event{Kind: EventSessionEnd})
	now = now.Add(MinPublishInterval)
	publisher.Step(context.Background())
	if got := lastPublished(t, sink); got.Context != nil {
		t.Errorf("context after the session ended = %+v, want none", got.Context)
	}
}

func TestATranscriptThatIsNotAJSONLFileIsIgnored(t *testing.T) {
	tracker := NewTracker(start)
	tracker.Apply(Event{Kind: EventPrompt, Transcript: "/etc/passwd"}, start)
	if tracker.transcript != "" {
		t.Errorf("transcript = %q, want it ignored", tracker.transcript)
	}
}

func lastPublished(t *testing.T, sink *fakeSink) Record {
	t.Helper()
	if len(sink.published) == 0 {
		t.Fatal("nothing published")
	}
	return sink.published[len(sink.published)-1]
}
