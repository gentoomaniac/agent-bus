package adapter

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// waitResult is what one WaitNewMail call returned.
type waitResult struct {
	fresh []Message
	err   error
}

func startWaiting(ctx context.Context, adapter *Adapter) <-chan waitResult {
	results := make(chan waitResult, 1)
	go func() {
		fresh, _, err := adapter.WaitNewMail(ctx)
		results <- waitResult{fresh, err}
	}()
	return results
}

func TestWaitNewMailWakesOnTargetedMailOnly(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(carol)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	results := startWaiting(context.Background(), bobAdapter)

	publishTo(t, a, "general", Outgoing{Type: "message", Body: envelope.Body{Text: "chatter"}, Targets: []string{carol}})
	poll(t, bobAdapter)
	select {
	case result := <-results:
		t.Fatalf("woken by chatter: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	id, _ := a.Send(context.Background(), bob, text("for you"))
	poll(t, bobAdapter)
	select {
	case result := <-results:
		if result.err != nil || len(result.fresh) != 1 || result.fresh[0].Envelope.ID != id {
			t.Fatalf("woken with %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not woken by targeted mail")
	}
	if fresh, _, _ := bobAdapter.NewMail(); len(fresh) != 0 {
		t.Errorf("the mail that woke the waiter is delivered again: %v", fresh)
	}
}

func TestWaitNewMailStaysQuietForMailTheStopHookToldAlready(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	a.Send(context.Background(), bob, text("first"))
	poll(t, bobAdapter)
	if fresh, _, _ := bobAdapter.NewMail(); len(fresh) != 1 {
		t.Fatalf("the Stop hook was not told of the mail: %v", fresh)
	}

	select {
	case result := <-startWaiting(context.Background(), bobAdapter):
		t.Fatalf("woken for mail already delivered: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWaitNewMailReturnsAtOnceForMailThatWaits(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	a.Send(context.Background(), bob, text("first"))
	poll(t, bobAdapter)

	if result := <-startWaiting(context.Background(), bobAdapter); result.err != nil || len(result.fresh) != 1 {
		t.Errorf("waiting with mail: %+v", result)
	}
}

func TestWaitNewMailHoldsBackForTheCooldownThenWakes(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	var skewNanos atomic.Int64 // the waiter reads the clock while the test moves it
	bobAdapter, _ := b.open(bob, func(config *Config) {
		config.Now = func() time.Time { return b.now.Add(time.Duration(skewNanos.Load())) }
	})
	bobAdapter.MarkNotified(nil) // a notification just now
	a.Send(context.Background(), bob, text("soon"))
	poll(t, bobAdapter)
	results := startWaiting(context.Background(), bobAdapter)

	select {
	case result := <-results:
		t.Fatalf("woken inside the cooldown: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	// The cooldown runs on the adapter's clock: move it, then let the arrival of more mail look again.
	skewNanos.Store(int64(notifyCooldown + time.Second))
	a.Send(context.Background(), bob, text("later"))
	poll(t, bobAdapter)
	select {
	case result := <-results:
		if result.err != nil || len(result.fresh) != 2 {
			t.Errorf("after the cooldown: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not woken after the cooldown")
	}
}

func TestWaitNewMailEndsWithItsContext(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	bobAdapter, _ := b.open(bob)
	ctx, cancel := context.WithCancel(context.Background())
	results := startWaiting(ctx, bobAdapter)
	cancel()
	select {
	case result := <-results:
		if result.err == nil || len(result.fresh) != 0 {
			t.Errorf("result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait outlived its context")
	}
}
