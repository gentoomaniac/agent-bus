package adapter

import (
	"context"
	"testing"
	"time"
)

func TestNewMailNotifiesEachMessageOnceAndHonoursTheCooldown(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	ctx := context.Background()

	if fresh, unread, err := bobAdapter.NewMail(); err != nil || len(fresh) != 0 || unread != 0 {
		t.Fatalf("empty inbox: %d new, %d unread, %v", len(fresh), unread, err)
	}
	first, _ := a.Send(ctx, bob, text("one"))
	poll(t, bobAdapter)
	fresh, unread, err := bobAdapter.NewMail()
	if err != nil || len(fresh) != 1 || fresh[0].Envelope.ID != first || unread != 1 {
		t.Fatalf("first mail: %v, %d unread, %v", fresh, unread, err)
	}
	if fresh, unread, _ := bobAdapter.NewMail(); len(fresh) != 0 || unread != 1 {
		t.Errorf("told twice: %d new, %d unread", len(fresh), unread)
	}

	second, _ := a.Send(ctx, bob, text("two"))
	poll(t, bobAdapter)
	if fresh, _, _ := bobAdapter.NewMail(); len(fresh) != 0 {
		t.Errorf("a notification inside the cooldown: %v", fresh)
	}
	b.now = b.now.Add(notifyCooldown + time.Second)
	fresh, unread, _ = bobAdapter.NewMail()
	if len(fresh) != 1 || fresh[0].Envelope.ID != second || unread != 2 {
		t.Errorf("held-back mail after the cooldown: %v, %d unread", fresh, unread)
	}
}

func TestNewMailSkipsMailTheAgentAlreadyRead(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	ctx := context.Background()

	read, _ := a.Send(ctx, bob, text("read with inbox"))
	poll(t, bobAdapter)
	if listed, err := bobAdapter.Inbox(InboxQuery{}); err != nil || len(listed) != 1 {
		t.Fatalf("inbox: %v, %v", listed, err)
	}
	if fresh, unread, _ := bobAdapter.NewMail(); len(fresh) != 0 || unread != 1 {
		t.Errorf("mail read with inbox announced as new: %v, %d unread", fresh, unread)
	}
	if fresh, _, _ := bobAdapter.PeekNewMail(); len(fresh) != 0 {
		t.Errorf("a peek announces read mail: %v", fresh)
	}

	unseen, _ := a.Send(ctx, bob, text("not read yet"))
	poll(t, bobAdapter)
	if fresh, _, _ := bobAdapter.NewMail(); len(fresh) != 1 || fresh[0].Envelope.ID != unseen {
		t.Errorf("only the unread message is new: %v", fresh)
	}
	if summary, _ := bobAdapter.UnreadSummary(); len(summary) != 2 || summary[0].Envelope.ID != read {
		t.Errorf("the session summary lists everything unread: %v", summary)
	}
}

func TestPeekNewMailMarksNothingUntilMarkNotified(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	first, _ := a.Send(context.Background(), bob, text("one"))
	poll(t, bobAdapter)

	for range 2 {
		if fresh, _, _ := bobAdapter.PeekNewMail(); len(fresh) != 1 || fresh[0].Envelope.ID != first {
			t.Fatalf("a peek must keep showing the mail: %v", fresh)
		}
	}
	bobAdapter.MarkNotified([]string{first})
	if fresh, _, _ := bobAdapter.PeekNewMail(); len(fresh) != 0 {
		t.Errorf("told mail still new: %v", fresh)
	}
	second, _ := a.Send(context.Background(), bob, text("two"))
	poll(t, bobAdapter)
	if fresh, _, _ := bobAdapter.PeekNewMail(); len(fresh) != 0 {
		t.Errorf("the cooldown after MarkNotified was not honoured: %v", fresh)
	}
	b.now = b.now.Add(notifyCooldown + time.Second)
	if fresh, _, _ := bobAdapter.PeekNewMail(); len(fresh) != 1 || fresh[0].Envelope.ID != second {
		t.Errorf("after the cooldown: %v", fresh)
	}
}

func TestUnreadSummaryListsToldMailToo(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	a.Send(context.Background(), bob, text("one"))
	poll(t, bobAdapter)

	if _, _, err := bobAdapter.NewMail(); err != nil {
		t.Fatal(err)
	}
	summary, err := bobAdapter.UnreadSummary()
	if err != nil || len(summary) != 1 {
		t.Fatalf("summary: %v, %v", summary, err)
	}
	if fresh, _, _ := bobAdapter.NewMail(); len(fresh) != 0 {
		t.Error("the summary left mail un-notified")
	}
}

func TestNotificationsOfAnAdapterWithoutTheLockFail(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.open(alice)
	second, _ := b.open(alice)
	if _, _, err := second.NewMail(); err == nil {
		t.Error("NewMail without the lock succeeded")
	}
	if _, err := second.UnreadSummary(); err == nil {
		t.Error("UnreadSummary without the lock succeeded")
	}
}
