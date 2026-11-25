//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

func TestPresenceStorage(t *testing.T) {
	h := startBus(t)
	aliceTransport, bobTransport := h.transport(t, alice), h.transport(t, bob)
	admin := h.admin(t)

	t.Run("stream PRESENCE keeps the last record of each agent", func(t *testing.T) {
		stream, err := h.rawAdmin(t, broker.BusAdminUser).js.Stream(timeout(t), "PRESENCE")
		if err != nil {
			t.Fatal(err)
		}
		config := stream.CachedInfo().Config
		if got := strings.Join(config.Subjects, " "); got != "presence.*" || config.MaxMsgsPerSubject != 1 || !config.AllowDirect || config.Storage != jetstream.FileStorage {
			t.Errorf("PRESENCE = subjects %q, max per subject %d, direct %v, storage %v", got, config.MaxMsgsPerSubject, config.AllowDirect, config.Storage)
		}
	})

	t.Run("an agent publishes its own record and every agent and the admin read it", func(t *testing.T) {
		if record, err := bobTransport.Presence(timeout(t), alice); err != nil || record != nil {
			t.Fatalf("before any publish: %q, %v", record, err)
		}
		for _, state := range []string{`{"state":"idle"}`, `{"state":"working"}`} {
			if err := aliceTransport.PublishPresence([]byte(state)); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "alice's last record to be readable", func() bool {
			record, err := bobTransport.Presence(timeout(t), alice)
			return err == nil && string(record) == `{"state":"working"}`
		})
		if record, err := admin.Presence(timeout(t), alice); err != nil || string(record) != `{"state":"working"}` {
			t.Errorf("admin reads %q, %v", record, err)
		}
		if record, err := aliceTransport.Presence(timeout(t), bob); err != nil || record != nil {
			t.Errorf("bob never published: %q, %v", record, err)
		}
	})

	t.Run("an agent cannot write another agent's presence", func(t *testing.T) {
		bobAgent := h.rawAgent(t, bob)
		bobAgent.denyPublish(t, "presence."+bus.AgentToken(alice))
		bobAgent.denyPublish(t, "presence.anyone")
		time.Sleep(200 * time.Millisecond)
		if record, _ := admin.Presence(timeout(t), alice); string(record) != `{"state":"working"}` {
			t.Errorf("alice's record is now %q", record)
		}
		bobAgent.expectNoViolation(t, "bob's own presence", func() { bobAgent.conn.Publish("presence."+bus.AgentToken(bob), []byte(`{"state":"idle"}`)) })
	})

	t.Run("an agent still cannot read the message log directly", func(t *testing.T) {
		h.rawAgent(t, bob).denyRequest(t, "$JS.API.DIRECT.GET.BUS", `{"seq":1}`)
	})

	t.Run("the admin is told of every record as it is published", func(t *testing.T) {
		records := make(chan string, 4)
		stop, err := admin.WatchPresence(func(agentID string, record []byte) { records <- agentID + " " + string(record) })
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		if err := bobTransport.PublishPresence([]byte(`{"state":"idle"}`)); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-records:
			if got != bob+` {"state":"idle"}` {
				t.Errorf("watch saw %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the watch saw nothing")
		}
	})
}

// waitFor polls until the condition holds or the operation timeout passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An agent may direct-get presence records, and the reply subject of a request is the requester's
// choice. Pointing it into BUS must not put anything in front of a receiver as mail.
func TestPresenceDirectGetRedirectedIntoBus(t *testing.T) {
	h := startBus(t)
	if err := h.transport(t, alice).PublishPresence([]byte(`{"state":"working"}`)); err != nil {
		t.Fatal(err)
	}
	bobSession, _ := h.startAdapter(t, bob)
	busStream, err := h.rawAdmin(t, broker.BusAdminUser).js.Stream(timeout(t), "BUS")
	if err != nil {
		t.Fatal(err)
	}
	storedBefore := func() uint64 {
		info, err := busStream.Info(timeout(t))
		if err != nil {
			t.Fatal(err)
		}
		return info.State.Msgs
	}
	before := storedBefore()

	mallory := h.rawAgent(t, alice)
	get := "$JS.API.DIRECT.GET.PRESENCE.presence." + bus.AgentToken(alice)
	for _, replyTo := range []string{"dm." + bus.AgentToken(bob), "general"} {
		mallory.expectNoViolation(t, "direct get replying to "+replyTo, func() {
			if err := mallory.conn.PublishRequest(get, replyTo, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	time.Sleep(500 * time.Millisecond)

	stored := storedBefore() - before
	t.Logf("BUS holds %d new messages after the redirected direct gets", stored)
	if err := bobSession.Poll(timeout(t)); err != nil {
		t.Fatal(err)
	}
	if messages, err := bobSession.Inbox(adapter.InboxQuery{}); err != nil || len(messages) != 0 {
		t.Errorf("bob has mail from the redirected direct get: %v, %v", summaries(messages), err)
	}
}
