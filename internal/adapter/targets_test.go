package adapter

import (
	"context"
	"slices"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

const carol = "docs@carol"

func deliverAll(config *Config) { config.Deliver = DeliverAll }

func publishTo(t *testing.T, from *Adapter, channel string, message Outgoing) string {
	t.Helper()
	id, err := from.Publish(context.Background(), channel, message)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func inboxIDs(t *testing.T, adapter *Adapter) []string {
	t.Helper()
	var ids []string
	for _, message := range inboxOf(t, adapter) {
		ids = append(ids, message.Envelope.ID)
	}
	return ids
}

func TestTargetedModeDropsChatterAndDeliversWhatTargetsMe(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob)

	chatter := publishTo(t, a, "general", text("anyone around?"))
	toOther := publishTo(t, a, "general", Outgoing{Type: "message", Body: envelope.Body{Text: "x"}, Targets: []string{carol}})
	direct, _ := a.Send(context.Background(), bob, text("psst"))
	targeted := publishTo(t, a, "general", Outgoing{Type: "request", Body: envelope.Body{Text: "do xyz"}, Targets: []string{bob}})
	everyone := publishTo(t, a, "general", Outgoing{Type: "message", Body: envelope.Body{Text: "hi"}, Targets: []string{envelope.TargetAll}})
	mentioned := publishTo(t, a, "general", text("@lib-x@bob the text names the target"))
	poll(t, bobAdapter)

	got := inboxIDs(t, bobAdapter)
	want := []string{direct, targeted, everyone, mentioned}
	if len(got) != len(want) {
		t.Fatalf("inbox = %v, want the DM, the targeted, @all and the mentioned message", got)
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			t.Errorf("%s is missing from the inbox %v", id, got)
		}
	}
	if bobTransport.acks != 2 {
		t.Errorf("the dropped messages were acked %d times, want 2", bobTransport.acks)
	}
	for _, id := range []string{chatter, toOther} {
		if _, handled := bobAdapter.handled.messageByID(id); !handled {
			t.Errorf("dropped message %s is not in the handled set", id)
		}
	}
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != len(want) {
		t.Error("a dropped message came back")
	}
}

func TestDeliverAllKeepsChatterInTheInboxUntargeted(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	chatter := publishTo(t, a, "general", text("anyone around?"))
	poll(t, bobAdapter)

	messages := inboxOf(t, bobAdapter)
	if len(messages) != 1 || messages[0].Envelope.ID != chatter || messages[0].TargetsMe {
		t.Errorf("inbox = %+v", messages)
	}
}

func TestReplyToOwnMessageTargetsMeAfterARestart(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)

	question := publishTo(t, bobAdapter, "general", Outgoing{Type: "question", Body: envelope.Body{Text: "who knows?"}})
	bobAdapter.Close()
	poll(t, a)
	reply := publishTo(t, a, "general", Outgoing{Type: "answer", Body: envelope.Body{Text: "alice does"}, ReplyTo: question})
	// Alice's own message is not bob's, so a reply to it is chatter to him.
	elsewhere := publishTo(t, a, "general", Outgoing{Type: "answer", Body: envelope.Body{Text: "no"}, ReplyTo: reply})

	restarted, _ := b.open(bob)
	poll(t, restarted)
	got := inboxIDs(t, restarted)
	if !slices.Equal(got, []string{reply}) {
		t.Errorf("inbox = %v, want only the reply to bob's question, not %s", got, elsewhere)
	}
}

func TestHooksTellOnlyOfMailThatTargetsTheAgent(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	publishTo(t, a, "general", text("chatter"))
	request := publishTo(t, a, "general", Outgoing{Type: "request", Body: envelope.Body{Text: "do xyz"}, Targets: []string{bob}})
	poll(t, bobAdapter)

	fresh, unread, err := bobAdapter.NewMail()
	if err != nil || len(fresh) != 1 || fresh[0].Envelope.ID != request || unread != 2 {
		t.Errorf("new mail = %v, %d unread, %v", fresh, unread, err)
	}
	summary, err := bobAdapter.UnreadSummary()
	if err != nil || len(summary) != 1 || summary[0].Envelope.ID != request {
		t.Errorf("summary = %v, %v", summary, err)
	}
}

func TestInboxListsTargetedMailFirstAndLimitsAfterwards(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	publishTo(t, a, "general", text("chatter"))
	first := publishTo(t, a, "general", Outgoing{Type: "request", Body: envelope.Body{Text: "one"}, Targets: []string{bob}})
	second := publishTo(t, a, "general", Outgoing{Type: "request", Body: envelope.Body{Text: "two"}, Targets: []string{bob}})
	poll(t, bobAdapter)

	messages, err := bobAdapter.Inbox(InboxQuery{Limit: 2})
	if err != nil || len(messages) != 2 || messages[0].Envelope.ID != first || messages[1].Envelope.ID != second {
		t.Errorf("inbox = %v, %v", messages, err)
	}
	if bobAdapter.recent.id != second {
		t.Errorf("the reply parent is %s, want the newest targeted message %s", bobAdapter.recent.id, second)
	}
	all := inboxOf(t, bobAdapter)
	if len(all) != 3 || all[2].TargetsMe {
		t.Errorf("untargeted mail should list last: %v", all)
	}
}

func TestInvalidTargetsAreRefusedOnSend(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	a, _ := b.open(alice)
	_, err := a.Publish(context.Background(), "general", Outgoing{Type: "message", Body: envelope.Body{Text: "x"}, Targets: []string{"nobody"}})
	if err == nil {
		t.Error("an invalid target was sent")
	}
}
