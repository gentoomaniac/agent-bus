//go:build integration

package itest

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

const (
	testHeartbeat    = 500 * time.Millisecond
	testOfflineAfter = 2 * time.Second
)

// runEvent runs `agent-bus event` the way a harness hook does: a child of this process, which is
// also the parent of the MCP server started with startMCPServer. It must print nothing and succeed.
func (h *harness) runEvent(t *testing.T, agentBus, kind, hookJSON string) {
	t.Helper()
	cmd := exec.Command(agentBus, "event", "--kind="+kind)
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(hookJSON)
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("agent-bus event --kind=%s: %v, printed %q", kind, err, output)
	}
}

func presenceFor(t *testing.T, server *httptest.Server, agentID string) monitor.Presence {
	t.Helper()
	for _, view := range getJSON[[]monitor.Presence](t, server, "/api/v1/presence") {
		if view.Agent == agentID {
			return view
		}
	}
	return monitor.Presence{}
}

// waitForPresence polls the monitor's API until the agent's presence satisfies the condition.
func waitForPresence(t *testing.T, server *httptest.Server, agentID string, condition func(monitor.Presence) bool) monitor.Presence {
	t.Helper()
	var view monitor.Presence
	waitFor(t, "the presence of "+agentID, func() bool {
		view = presenceFor(t, server, agentID)
		return condition(view)
	})
	return view
}

func TestPresenceEndToEnd(t *testing.T) {
	agentBus := filepath.Join(t.TempDir(), "agent-bus")
	build := exec.Command("go", "build", "-o", agentBus, "../../cmd/agent-bus")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build agent-bus: %v\n%s", err, output)
	}
	h := startBus(t)
	h.register(t, monitorAgent)

	web := monitor.New(monitor.Config{
		Admin: h.admin(t), History: h.transportOnAdmin(t), OfflineAfter: testOfflineAfter,
		Report: func(err error) { t.Errorf("monitor: %v", err) },
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { web.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = web.Handler(server.Listener.Addr().String())
	server.Start()
	t.Cleanup(server.Close)

	mcpCmd := exec.Command(agentBus, "mcp", "--agent", bob)
	mcpCmd.Env = append(h.env, "BUS_PRESENCE_HEARTBEAT="+testHeartbeat.String())
	bobSession := h.connectMCP(t, mcpCmd)
	aliceSession, _ := h.startAdapter(t, alice)

	t.Run("the event command is silent and succeeds where no session listens", func(t *testing.T) {
		cmd := exec.Command(agentBus, "event", "--kind=stop-failure")
		cmd.Env = append(h.env, "XDG_RUNTIME_DIR="+t.TempDir())
		cmd.Stdin = strings.NewReader("{broken")
		if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
			t.Errorf("event without a session: %v, printed %q", err, output)
		}
	})

	t.Run("a session that just started is idle", func(t *testing.T) {
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.Idle })
	})

	t.Run("hook events show up as state, activity and workers", func(t *testing.T) {
		h.runEvent(t, agentBus, "user-prompt", `{"prompt":"do the secret thing"}`)
		h.runEvent(t, agentBus, "pre-tool", `{"tool_name":"Edit","tool_input":{"file_path":"/work/project/main.go","new_string":"SECRET"}}`)
		h.runEvent(t, agentBus, "pre-tool", `{"tool_name":"Agent","tool_input":{"description":"review the diff"}}`)
		h.runEvent(t, agentBus, "subagent-start", `{"agent_id":"a1","agent_type":"Explore"}`)
		h.runEvent(t, agentBus, "pre-tool", `{"tool_name":"Bash","tool_input":{"command":"go test ./... --token SECRET"}}`)
		view := waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.Working && len(view.Workers) == 1 })
		if view.Activity != "Bash go" || view.Workers[0].Description != "review the diff" || view.ActivitySince.IsZero() {
			t.Errorf("presence = %+v", view)
		}
		if strings.Contains(strings.Join(entryTexts(view.Recent), "|")+view.Activity, "SECRET") {
			t.Errorf("content leaked into presence: %+v", view)
		}
		if len(view.Recent) != 3 {
			t.Errorf("recent = %+v, want two tool starts and the worker", view.Recent)
		}

		// Another agent reads it through the bus as list_agents does.
		record, known, err := aliceSession.Presence(timeout(t), bob)
		if err != nil || !known || record.State != presence.Working {
			t.Errorf("alice reads %+v, %v, %v", record, known, err)
		}

		h.runEvent(t, agentBus, "subagent-stop", `{"agent_id":"a1","agent_type":"Explore"}`)
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return len(view.Workers) == 0 })
	})

	t.Run("an error that ends a turn shows as a state", func(t *testing.T) {
		h.runEvent(t, agentBus, "stop-failure", `{"error":"rate_limit"}`)
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.RateLimited })
		h.runEvent(t, agentBus, "stop-failure", `{"error":"server_error"}`)
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == "error:server_error" })
		h.runEvent(t, agentBus, "notification", `{"notification_type":"permission_prompt"}`)
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.WaitingForOwner })
	})

	t.Run("another agent cannot write this agent's presence", func(t *testing.T) {
		h.rawAgent(t, alice).denyPublish(t, "presence."+bus.AgentToken(bob))
		if view := presenceFor(t, server, bob); view.State != presence.WaitingForOwner {
			t.Errorf("bob's presence changed to %+v", view)
		}
	})

	t.Run("SessionEnd shows offline at once", func(t *testing.T) {
		h.runEvent(t, agentBus, "session-end", `{"reason":"logout"}`)
		view := waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.Offline })
		if view.Activity != "" || len(view.Workers) != 0 {
			t.Errorf("offline presence still shows activity: %+v", view)
		}
		h.runEvent(t, agentBus, "session-start", `{"source":"resume"}`)
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.Idle })
	})

	t.Run("a killed process is offline once its heartbeat is overdue", func(t *testing.T) {
		killedAt := time.Now()
		if err := bobSession.cmd.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitForPresence(t, server, bob, func(view monitor.Presence) bool { return view.State == presence.Offline })
		if elapsed := time.Since(killedAt); elapsed < testOfflineAfter-testHeartbeat-time.Second {
			t.Errorf("offline after only %s: the last heartbeat cannot have run out", elapsed)
		}
	})
}

func entryTexts(entries []presence.Entry) []string {
	var texts []string
	for _, entry := range entries {
		texts = append(texts, entry.Text)
	}
	return texts
}
