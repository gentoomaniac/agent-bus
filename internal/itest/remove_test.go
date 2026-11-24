//go:build integration

package itest

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
)

// TestAgentRemoval removes an agent from a running bus and follows it through every surface:
// its connection, its consumers, the other agents' trust and the monitor.
func TestAgentRemoval(t *testing.T) {
	h := startBus(t)
	h.register(t, monitorAgent)
	aliceSession, _ := h.startAdapter(t, alice)
	bobSession, _ := h.startAdapter(t, bob)
	ctx := timeout(t)

	if _, err := aliceSession.Send(ctx, bob, say("hello bob")); err != nil {
		t.Fatal(err)
	}
	firstID := pollUntil(t, bobSession, 1)[0].Envelope.ID
	if err := bobSession.Ack(ctx, []string{firstID}); err != nil {
		t.Fatal(err)
	}
	// Signed while alice's key is valid, read by bob only after the removal.
	if _, err := aliceSession.Send(ctx, bob, say("sent just before the removal")); err != nil {
		t.Fatal(err)
	}
	aliceConnection := h.rawAgent(t, alice)

	h.run(t, h.busctl, "--direct", "agent", "remove", alice)

	t.Run("drops the connection and refuses a new one", func(t *testing.T) {
		deadline := time.Now().Add(10 * time.Second)
		for !aliceConnection.conn.IsClosed() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !aliceConnection.conn.IsClosed() {
			t.Error("the removed agent's connection is still open")
		}
		if transport, err := broker.NewTransport(h.agentConnection(alice), alice); err == nil {
			transport.Close()
			t.Error("the removed agent connected again")
		}
	})

	t.Run("deletes its consumers", func(t *testing.T) {
		for name := range h.consumerPositions(t) {
			if strings.HasPrefix(name, bus.AgentToken(alice)+"__") {
				t.Errorf("consumer %s of the removed agent is still there", name)
			}
		}
	})

	t.Run("refuses to register the ID again and lists it as removed", func(t *testing.T) {
		output, err := h.tryRun(h.busctl, "--direct", "register", alice)
		if err == nil || !strings.Contains(output, "removed") {
			t.Errorf("register of a removed ID: %v\n%s", err, output)
		}
		list := h.run(t, h.busctl, "--direct", "agent", "list")
		statuses := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 {
				statuses[fields[0]] = fields[1]
			}
		}
		if statuses[alice] != "removed" || statuses[bob] != "active" {
			t.Errorf("agent list:\n%s", list)
		}
		h.run(t, h.busctl, "--direct", "agent", "remove", alice) // a rerun is safe
	})

	t.Run("the other agent rejects the removed key after a directory refresh", func(t *testing.T) {
		h.run(t, h.busctl, "--direct", "directory", "export", filepath.Join(h.configDir, "directory"))
		if err := bobSession.RefreshDirectory(ctx); err != nil {
			t.Fatal(err)
		}
		if messages := pollUntil(t, bobSession, 0); len(messages) != 0 {
			t.Errorf("bob has %v", summaries(messages))
		}
		if alerts := h.readFile(t, bob, "alerts.log"); !strings.Contains(alerts, "revoked key") {
			t.Errorf("bob's alerts: %q", alerts)
		}
		if _, err := bobSession.Send(ctx, alice, say("still there?")); err == nil || !strings.Contains(err.Error(), "agent removed") {
			t.Errorf("send to the removed agent: %v", err)
		}
	})

	t.Run("the monitor shows it as removed", func(t *testing.T) {
		sender, _ := h.startAdapter(t, monitorAgent)
		web := monitor.New(monitor.Config{
			Admin: h.admin(t), History: h.transportOnAdmin(t), Sender: sender,
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

		var removed []string
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && len(removed) == 0; time.Sleep(100 * time.Millisecond) {
			for _, agent := range getJSON[[]monitor.Agent](t, server, "/api/v1/agents") {
				if agent.Removed {
					removed = append(removed, agent.ID)
				}
			}
		}
		if !slices.Equal(removed, []string{alice}) {
			t.Errorf("removed agents in the monitor: %v", removed)
		}
	})
}
