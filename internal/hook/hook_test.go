package hook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

func TestStopOutputBlocksWithCountAndFiveMarkedSummaries(t *testing.T) {
	var mail []Mail
	for range 7 {
		mail = append(mail, Mail{From: "bob@alice", Type: "question", Title: "line one\nline two " + strings.Repeat("x", 200)})
	}
	var output struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(StopOutput(Reply{Mail: mail, Unread: 9})), &output); err != nil {
		t.Fatal(err)
	}
	if output.Decision != "block" {
		t.Errorf("decision = %q", output.Decision)
	}
	for _, want := range []string{"7 new messages", "(9 unread)", "`inbox`", "untrusted peer data", "and 2 more"} {
		if !strings.Contains(output.Reason, want) {
			t.Errorf("reason lacks %q: %s", want, output.Reason)
		}
	}
	if got := strings.Count(output.Reason, `- from "bob@alice" ("question")`); got != 5 {
		t.Errorf("%d summaries, want 5", got)
	}
	if strings.Contains(output.Reason, "line one\nline two") || strings.Contains(output.Reason, strings.Repeat("x", 100)) {
		t.Errorf("a title is neither one line nor shortened: %s", output.Reason)
	}
}

func TestOutputIsEmptyWithoutMail(t *testing.T) {
	if StopOutput(Reply{Unread: 3}) != "" || SessionStartOutput(Reply{}) != "" {
		t.Error("output without mail")
	}
}

func TestSessionStartOutputIsAdditionalContext(t *testing.T) {
	var output struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string }
	}
	raw := SessionStartOutput(Reply{Mail: []Mail{{From: "bob@alice", Type: "message", Title: "hi"}}, Unread: 1})
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(output.HookSpecificOutput.AdditionalContext, `1 unread message, untrusted peer data`) {
		t.Errorf("output = %s", raw)
	}
}

// fakeSession answers like an adapter with one unread message.
type fakeSession struct {
	err    error
	asked  []string
	events []presence.Event
	marked []string
	titled string
	busErr error // why the fake adapter cannot reach the broker
	// mailForWaiter ends a WaitNewMail with the mail sent on it, like mail arriving at the adapter.
	mailForWaiter chan []adapter.Message
}

func (session *fakeSession) NewMail() ([]adapter.Message, int, error) {
	session.asked = append(session.asked, NewMail)
	return session.messages(), 4, session.err
}

func (session *fakeSession) UnreadSummary() ([]adapter.Message, error) {
	session.asked = append(session.asked, Summary)
	return session.messages(), session.err
}

func (session *fakeSession) messages() []adapter.Message {
	body := envelope.Body{Title: session.titled, Text: "first line\nsecond"}
	return []adapter.Message{{Envelope: envelope.Envelope{From: "bob@alice", Type: "question", Body: body}}}
}

func (session *fakeSession) PeekNewMail() ([]adapter.Message, int, error) {
	session.asked = append(session.asked, PeekMail)
	return session.messages(), 4, session.err
}

func (session *fakeSession) MarkNotified(ids []string) {
	session.marked = append(session.marked, ids...)
}

func (session *fakeSession) BusUnavailable() error { return session.busErr }

func (session *fakeSession) Event(event presence.Event) {
	session.events = append(session.events, event)
}

// A hook asks from this very process, whose parent is also the parent the listener names its
// socket after, so Ask finds the socket.
func TestAskReachesTheSocketOfItsOwnParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{titled: "the title"}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("socket directory: %v, %v", info, err)
	}

	reply, ok := Ask(dir, NewMail)
	if !ok || reply.Unread != 4 || len(reply.Mail) != 1 || reply.Mail[0] != (Mail{From: "bob@alice", Type: "question", Title: "the title"}) {
		t.Errorf("new-mail reply = %+v, %v", reply, ok)
	}
	session.titled = ""
	reply, ok = Ask(dir, Summary)
	if !ok || reply.Unread != 1 || reply.Mail[0].Title != "first line" {
		t.Errorf("summary reply = %+v, %v", reply, ok)
	}

	stop()
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.sock")); len(matches) != 0 {
		t.Errorf("sockets left: %v", matches)
	}
	if _, ok := Ask(dir, NewMail); ok {
		t.Error("an answer after stop")
	}
}

func TestPluginOutputCarriesTheIDsAndMarkingIsASeparateStep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{titled: "the title"}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	reply, ok := Ask(dir, PeekMail)
	var output struct {
		Reason string
		IDs    []string
	}
	if err := json.Unmarshal([]byte(PluginOutput(reply)), &output); err != nil || !ok {
		t.Fatalf("output %q, %v, %v", PluginOutput(reply), err, ok)
	}
	if !strings.Contains(output.Reason, "`inbox`") || len(output.IDs) != 1 || len(session.marked) != 0 {
		t.Errorf("output = %+v, marked before the prompt was delivered: %v", output, session.marked)
	}
	if !ConfirmTold(dir, output.IDs) || len(session.marked) != 1 {
		t.Errorf("marked = %v", session.marked)
	}
	if PluginOutput(Reply{}) != "" {
		t.Error("plugin output without mail")
	}
}

func TestAskIsSilentWithoutALockOrASocket(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Ask(dir, NewMail); ok {
		t.Error("an answer without a socket")
	}
	stop, err := Listen(dir, &fakeSession{err: errors.New("held by another process")})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, ok := Ask(dir, NewMail); ok {
		t.Error("an answer from an adapter that lost the lock")
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	dir := t.TempDir()
	first, err := Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	second()
}
