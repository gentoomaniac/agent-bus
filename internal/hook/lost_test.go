package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLostServerIsToldOnceAfterTheAdapterEnds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if notice := LostServer(dir); notice != "" {
		t.Errorf("a session that never had a server was told %q", notice)
	}
	stop, err := Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	if notice := LostServer(dir); notice != "" {
		t.Errorf("a session with a running server was told %q", notice)
	}

	stop()
	notice := LostServer(dir)
	if !strings.Contains(notice, "/mcp") {
		t.Errorf("notice = %q, want it to name /mcp", notice)
	}
	if again := LostServer(dir); again != "" {
		t.Errorf("the loss was told twice: %q", again)
	}

	stop, err = Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if LostServer(dir) == "" {
		t.Error("the loss of the server that /mcp started was not told")
	}
}

func TestListenClearsMarkersOfEndedHarnesses(t *testing.T) {
	dir := t.TempDir()
	endedHarness := filepath.Join(dir, "2147483646"+markerSuffix) // above any PID the kernel hands out
	if err := os.WriteFile(endedHarness, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stop, err := Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := os.Stat(endedHarness); !os.IsNotExist(err) {
		t.Errorf("the marker of an ended harness stays: %v", err)
	}
}

func TestOutputsCarryAProblemWithoutMail(t *testing.T) {
	lost := Reply{Problem: "the MCP server is gone"}

	var stop struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(StopOutput(lost)), &stop); err != nil {
		t.Fatal(err)
	}
	if stop.Decision != "block" || !strings.Contains(stop.Reason, "Agent Bus: the MCP server is gone") {
		t.Errorf("stop output = %+v", stop)
	}
	if !strings.Contains(WakeOutput(lost), "Agent Bus: the MCP server is gone") {
		t.Errorf("wake output = %q", WakeOutput(lost))
	}
	if !strings.Contains(SessionStartOutput(lost), "the MCP server is gone") {
		t.Errorf("session start output = %q", SessionStartOutput(lost))
	}
	if PluginOutput(lost) != "" {
		t.Error("the plugin output carries a problem")
	}

	both := Reply{Mail: []Mail{{From: "bob@alice", Type: "message", Title: "hi"}}, Unread: 1, Problem: "the MCP server is gone"}
	if out := WakeOutput(both); !strings.Contains(out, "1 new message") || !strings.Contains(out, "the MCP server is gone") {
		t.Errorf("wake output with mail and a problem = %q", out)
	}
}

func TestWaitEndsWithAProblemWhenTheAdapterIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	stop, err := Listen(dir, &fakeSession{})
	if err != nil {
		t.Fatal(err)
	}
	stop()

	reply := expectReply(t, startWait(dir, time.Minute))
	if len(reply.Mail) != 0 || !strings.Contains(reply.Problem, "/mcp") {
		t.Errorf("reply = %+v", reply)
	}
	if reply := expectReply(t, startWait(dir, 200*time.Millisecond)); reply.Problem != "" {
		t.Errorf("the loss was told to a second waiter: %+v", reply)
	}
}
