//go:build integration

package itest

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// startAdapter runs the adapter of a registered agent on the real broker. stop gives the identity
// and the connection up, like ending a session; it is also called at the end of the test.
func (h *harness) startAdapter(t *testing.T, agentID string, groups ...string) (session *adapter.Adapter, stop func()) {
	t.Helper()
	dir := creds.AgentDir(h.configDir, agentID)
	transport, err := broker.NewTransport(h.agentConnection(agentID), agentID)
	if err != nil {
		t.Fatal(err)
	}
	session, err = adapter.Open(adapter.Config{AgentID: agentID, Dir: dir, Transport: transport, Directory: trust.DirectoryPath(h.configDir), Groups: groups})
	if err != nil {
		transport.Close()
		t.Fatal(err)
	}
	var stopped bool
	stop = func() {
		if !stopped {
			stopped = true
			session.Close()
			transport.Close()
		}
	}
	t.Cleanup(stop)
	return session, stop
}

// sayTo is a message aimed at agents, so a targeted-mode adapter delivers it.
func sayTo(text string, targets ...string) adapter.Outgoing {
	return adapter.Outgoing{Type: "message", Body: envelope.Body{Text: text}, Targets: targets}
}

func say(text string) adapter.Outgoing {
	return adapter.Outgoing{Type: "message", Body: envelope.Body{Text: text}}
}

// pollUntil polls until the agent has at least n unread messages and returns them.
func pollUntil(t *testing.T, session *adapter.Adapter, n int) []adapter.Message {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := session.Poll(timeout(t)); err != nil {
			t.Fatal(err)
		}
		messages, err := session.Inbox(adapter.InboxQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) >= n || time.Now().After(deadline) {
			if len(messages) != n {
				t.Fatalf("%d unread messages, want %d: %v", len(messages), n, summaries(messages))
			}
			return messages
		}
	}
}

func summaries(messages []adapter.Message) []string {
	var lines []string
	for _, message := range messages {
		lines = append(lines, message.Envelope.From+" on "+message.Channel.String()+": "+message.Envelope.Body.Text)
	}
	return lines
}

func (h *harness) readFile(t *testing.T, agentID, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(creds.AgentDir(h.configDir, agentID), name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// shortenAckWait makes the agent's DM consumer redeliver after two seconds instead of a minute, so
// a test can see a redelivery. Only the admin may change a consumer.
func (h *harness) shortenAckWait(t *testing.T, agentID string) {
	t.Helper()
	busAdmin := h.rawAdmin(t, broker.BusAdminUser)
	consumer, err := busAdmin.js.Consumer(timeout(t), "BUS", bus.AgentToken(agentID)+"__dm")
	if err != nil {
		t.Fatal(err)
	}
	config := consumer.CachedInfo().Config
	config.AckWait = 2 * time.Second
	if _, err := busAdmin.js.UpdateConsumer(timeout(t), "BUS", config); err != nil {
		t.Fatal(err)
	}
}

// pending counts the records of the agent's DM consumer that are not acknowledged.
func (h *harness) pending(t *testing.T, agentID string) int {
	t.Helper()
	busAdmin := h.rawAdmin(t, broker.BusAdminUser)
	consumer, err := busAdmin.js.Consumer(timeout(t), "BUS", bus.AgentToken(agentID)+"__dm")
	if err != nil {
		t.Fatal(err)
	}
	info, err := consumer.Info(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	return info.NumAckPending + int(info.NumPending)
}

// Every case starts its own bus: all agents read `general` from the beginning, so mail sent in one
// case would show up in the next one's inbox.
func TestMilestone2(t *testing.T) {
	t.Run("two agents exchange a DM and a general post, both verify and pin", func(t *testing.T) {
		h := startBus(t)
		a, _ := h.startAdapter(t, alice)
		b, _ := h.startAdapter(t, bob)
		ctx := timeout(t)

		dmID, err := a.Send(ctx, bob, say("hello bob"))
		if err != nil {
			t.Fatal(err)
		}
		postID, err := a.Publish(ctx, "general", sayTo("hello everyone", bob))
		if err != nil {
			t.Fatal(err)
		}
		messages := pollUntil(t, b, 2)
		got := map[string]adapter.Message{}
		for _, message := range messages {
			got[message.Envelope.ID] = message
		}
		if got[dmID].Envelope.Body.Text != "hello bob" || got[postID].Envelope.Body.Text != "hello everyone" {
			t.Errorf("bob's inbox = %v", summaries(messages))
		}
		if got[dmID].Channel != bus.DMChannel(bob) || got[postID].Channel != bus.GeneralChannel() {
			t.Errorf("channels = %v, %v", got[dmID].Channel, got[postID].Channel)
		}
		if !strings.Contains(h.readFile(t, bob, "known_agents"), alice+" "+a.Fingerprint()) {
			t.Errorf("bob did not pin alice: %q", h.readFile(t, bob, "known_agents"))
		}
		if err := b.Ack(ctx, []string{dmID, postID}); err != nil {
			t.Fatal(err)
		}

		// Bob answers; alice verifies and pins bob. Her own general post is not delivered back.
		hello := say("hello alice")
		hello.ReplyTo = dmID
		if _, err := b.Send(ctx, alice, hello); err != nil {
			t.Fatal(err)
		}
		reply := pollUntil(t, a, 1)
		if reply[0].Envelope.From != bob || !reply[0].FirstContact || reply[0].Envelope.Hops != 1 {
			t.Errorf("alice's inbox = %v", summaries(reply))
		}
		if !strings.Contains(h.readFile(t, alice, "known_agents"), bob+" "+b.Fingerprint()) {
			t.Errorf("alice did not pin bob: %q", h.readFile(t, alice, "known_agents"))
		}
	})

	t.Run("an agent joined to a group receives a post of another agent", func(t *testing.T) {
		h := startBus(t)
		h.run(t, h.busctl, "--direct", "channel", "join", bob, "lib-x")
		a, _ := h.startAdapter(t, alice)
		b, _ := h.startAdapter(t, bob, "lib-x")

		postID, err := a.Publish(timeout(t), "lib-x", sayTo("hello lib-x", bob))
		if err != nil {
			t.Fatal(err)
		}
		messages := pollUntil(t, b, 1)
		if messages[0].Envelope.ID != postID || messages[0].Channel != bus.GroupChannel("lib-x") {
			t.Errorf("bob's inbox = %v", summaries(messages))
		}
	})

	t.Run("a tampered message is rejected, alerted and acknowledged", func(t *testing.T) {
		h := startBus(t)
		const mallory, victim = "mallory@alice", "victim@alice"
		h.register(t, mallory)
		h.register(t, victim)
		v, _ := h.startAdapter(t, victim)

		// A message signed correctly by mallory, then changed on its way into the stream.
		key, err := identity.ReadPrivateKey(creds.AgentDir(h.configDir, mallory))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := envelope.Sign(envelope.Envelope{
			Version: envelope.Version, ID: envelope.NewID(time.Now()), TS: time.Now().UTC().Truncate(time.Second), From: mallory,
			To: envelope.ToFor(bus.DMChannel(victim)), Type: "message", Targets: []string{victim}, Thread: "t", KeyFP: identity.Fingerprint(key.Public().(ed25519.PublicKey)),
			Body: envelope.Body{Text: "transfer 10"},
		}, key)
		if err != nil {
			t.Fatal(err)
		}
		tampered := []byte(strings.Replace(string(payload), "transfer 10", "transfer 99", 1))
		if _, err := h.transport(t, mallory).Publish(timeout(t), bus.DMChannel(victim), "tampered-1", tampered); err != nil {
			t.Fatal(err)
		}

		if err := v.Poll(timeout(t)); err != nil {
			t.Fatal(err)
		}
		if messages, _ := v.Inbox(adapter.InboxQuery{}); len(messages) != 0 {
			t.Errorf("a tampered message was delivered: %v", summaries(messages))
		}
		if alerts := h.readFile(t, victim, "alerts.log"); !strings.Contains(alerts, "does not verify") {
			t.Errorf("alerts.log = %q", alerts)
		}
		if got := h.pending(t, victim); got != 0 {
			t.Errorf("%d records still pending on the broker, want the tampered one acked", got)
		}
	})

	t.Run("a duplicate is skipped, on publish and on receive", func(t *testing.T) {
		h := startBus(t)
		const sender, receiver = "dup-sender@alice", "dup-receiver@alice"
		h.register(t, sender)
		h.register(t, receiver)
		h.shortenAckWait(t, receiver)
		s, _ := h.startAdapter(t, sender)
		r, _ := h.startAdapter(t, receiver)
		ctx := timeout(t)

		// Publishing the same ID twice stores it once and returns the first sequence.
		rawSender := h.transport(t, sender)
		id := envelope.NewID(time.Now())
		first, err := rawSender.Publish(ctx, bus.DMChannel(receiver), id, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if again, err := rawSender.Publish(ctx, bus.DMChannel(receiver), id, []byte("x")); err != nil || again != first {
			t.Errorf("repeated publish = %d, %v; want the first sequence %d", again, err, first)
		}
		// The receiver alerts on the unparseable record and acks it; it does not stall.
		if err := r.Poll(ctx); err != nil {
			t.Fatal(err)
		}

		// The same signed record stored again under another message ID (after the dedupe window,
		// say) reaches the receiver once.
		sentID, err := s.Send(ctx, receiver, say("only once"))
		if err != nil {
			t.Fatal(err)
		}
		history, err := h.transportOnAdmin(t).History(ctx, bus.DMChannel(receiver), first, 10)
		if err != nil || len(history) == 0 {
			t.Fatalf("history: %v, %v", history, err)
		}
		if _, err := rawSender.Publish(ctx, bus.DMChannel(receiver), "replayed-"+sentID, history[len(history)-1].Payload); err != nil {
			t.Fatal(err)
		}
		messages := pollUntil(t, r, 1)
		if messages[0].Envelope.ID != sentID {
			t.Fatalf("delivered %v", summaries(messages))
		}
		if err := r.Ack(ctx, []string{sentID}); err != nil {
			t.Fatal(err)
		}
		// The copy sat unacknowledged next to the buffered original; once the broker redelivers it
		// the handled set recognises it and it is acked.
		time.Sleep(3 * time.Second)
		if err := r.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		if again, _ := r.Inbox(adapter.InboxQuery{}); len(again) != 0 {
			t.Errorf("the replayed copy was delivered: %v", summaries(again))
		}
		if got := h.pending(t, receiver); got != 0 {
			t.Errorf("%d records pending after the replayed copy was skipped", got)
		}
	})

	t.Run("ack B before A, restart: A comes back, B does not", func(t *testing.T) {
		h := startBus(t)
		const sender, receiver = "order-sender@alice", "order-receiver@alice"
		h.register(t, sender)
		h.register(t, receiver)
		h.shortenAckWait(t, receiver)
		s, _ := h.startAdapter(t, sender)
		r, stop := h.startAdapter(t, receiver)
		ctx := timeout(t)

		idA, err := s.Send(ctx, receiver, say("A"))
		if err != nil {
			t.Fatal(err)
		}
		idB, err := s.Send(ctx, receiver, say("B"))
		if err != nil {
			t.Fatal(err)
		}
		pollUntil(t, r, 2)
		if err := r.Ack(ctx, []string{idB}); err != nil {
			t.Fatal(err)
		}

		stop() // the session ends with A unacknowledged
		time.Sleep(3 * time.Second)
		restarted, _ := h.startAdapter(t, receiver)
		messages := pollUntil(t, restarted, 1)
		if messages[0].Envelope.ID != idA {
			t.Errorf("after the restart the inbox holds %q, want A (%s)", messages[0].Envelope.Body.Text, idA)
		}
	})

	t.Run("a second adapter on the same identity fails every operation", func(t *testing.T) {
		h := startBus(t)
		const holder = "locked@alice"
		h.register(t, holder)
		first, stopFirst := h.startAdapter(t, holder)
		second, _ := h.startAdapter(t, holder)
		ctx := timeout(t)

		_, sendErr := second.Send(ctx, alice, say("x"))
		_, inboxErr := second.Inbox(adapter.InboxQuery{})
		for name, err := range map[string]error{"send": sendErr, "inbox": inboxErr, "poll": second.Poll(ctx), "ack": second.Ack(ctx, []string{"x"})} {
			var held identity.HeldError
			if !errors.As(err, &held) || !strings.Contains(err.Error(), holder) {
				t.Errorf("%s: %v, want the identity-in-use error", name, err)
			}
		}
		if _, err := first.Send(ctx, alice, say("the holder works")); err != nil {
			t.Errorf("the holder: %v", err)
		}

		stopFirst()
		third, _ := h.startAdapter(t, holder)
		if _, err := third.Send(ctx, alice, say("free again")); err != nil {
			t.Errorf("after the first session ended: %v", err)
		}
	})

	t.Run("the hop limit stops two auto-repliers", func(t *testing.T) {
		h := startBus(t)
		const left, right = "left@alice", "right@alice"
		h.register(t, left)
		h.register(t, right)
		l, _ := h.startAdapter(t, left)
		r, _ := h.startAdapter(t, right)
		ctx := timeout(t)

		if _, err := l.Send(ctx, right, say("ping")); err != nil {
			t.Fatal(err)
		}
		// Each side reads and answers, and every third answer is a post to general instead of a DM.
		// Every answer names reply_to: a parent inferred from reading is only used on the channel it
		// came from, so a change of channel would start a new conversation.
		var stopped error
		exchanged := 1
		for turn := 0; turn < 12 && stopped == nil; turn++ {
			replier, other := r, left
			if turn%2 == 1 {
				replier, other = l, right
			}
			messages := pollUntil(t, replier, 1)
			pong := say("pong")
			pong.ReplyTo = messages[0].Envelope.ID
			if turn%3 == 2 {
				pong.Targets = []string{envelope.TargetAll}
				_, stopped = replier.Publish(timeout(t), "general", pong)
			} else {
				_, stopped = replier.Send(timeout(t), other, pong)
			}
			if err := replier.Ack(ctx, []string{messages[0].Envelope.ID}); err != nil {
				t.Fatal(err)
			}
			if stopped == nil {
				exchanged++
			}
		}
		if stopped == nil || !strings.Contains(stopped.Error(), "limit") {
			t.Fatalf("the loop never stopped: %v", stopped)
		}
		if exchanged != 7 {
			t.Errorf("%d messages were exchanged, want the ping and 6 replies", exchanged)
		}
	})

	t.Run("the secret scan blocks a send", func(t *testing.T) {
		h := startBus(t)
		a, _ := h.startAdapter(t, alice)
		_, err := a.Send(timeout(t), bob, say("my key: -----BEGIN OPENSSH PRIVATE KEY-----"))
		if err == nil || !strings.Contains(err.Error(), "private key") {
			t.Errorf("err = %v", err)
		}
	})
}
