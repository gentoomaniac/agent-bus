package adapter

import (
	"context"
	"testing"
	"time"
)

func TestMailWaitingCountsTargetedMailAndTracksTheOldestUnseen(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	ctx := context.Background()

	if mail := bobAdapter.MailWaiting(); mail.Pending != 0 || !mail.OldestUnseen.IsZero() {
		t.Fatalf("empty inbox: %+v", mail)
	}
	arrived := b.now
	a.Send(ctx, bob, text("for you"))
	a.Publish(ctx, "general", Outgoing{Type: "message", Body: text("chatter").Body, Targets: []string{alice}})
	poll(t, bobAdapter)
	if mail := bobAdapter.MailWaiting(); mail.Pending != 1 || !mail.OldestUnseen.Equal(arrived) {
		t.Errorf("one targeted and one context message: %+v", mail)
	}

	b.now = b.now.Add(time.Minute)
	if _, err := bobAdapter.Inbox(InboxQuery{}); err != nil {
		t.Fatal(err)
	}
	if mail := bobAdapter.MailWaiting(); mail.Pending != 1 || !mail.OldestUnseen.IsZero() {
		t.Errorf("after the agent listed its inbox: %+v", mail)
	}
}

func TestInboxNewOnlySkipsWhatWasListedBefore(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	ctx := context.Background()

	a.Send(ctx, bob, text("one"))
	poll(t, bobAdapter)
	if first, _ := bobAdapter.Inbox(InboxQuery{NewOnly: true}); len(first) != 1 {
		t.Fatalf("first listing: %v", first)
	}
	if again, _ := bobAdapter.Inbox(InboxQuery{NewOnly: true}); len(again) != 0 {
		t.Errorf("listed again: %v", again)
	}
	a.Send(ctx, bob, text("two"))
	poll(t, bobAdapter)
	if fresh, _ := bobAdapter.Inbox(InboxQuery{NewOnly: true}); len(fresh) != 1 || fresh[0].Envelope.Body.Text != "two" {
		t.Errorf("new mail: %v", fresh)
	}
	if all, _ := bobAdapter.Inbox(InboxQuery{}); len(all) != 2 {
		t.Errorf("a plain listing must still show everything: %v", all)
	}
}
