package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func TestStatusShowsWhetherTheBusIsReachedAndReportsNoOutage(t *testing.T) {
	fake := newFakeBus(t)
	var reported []error
	monitor := New(Config{Admin: fake, History: fake, Report: func(err error) { reported = append(reported, err) }})
	if status := monitor.Status(); status.Bus != BusLive {
		t.Errorf("bus = %q, want %q", status.Bus, BusLive)
	}

	fake.down = bus.ErrUnavailable
	if status := monitor.Status(); status.Bus != BusReconnecting {
		t.Errorf("bus = %q, want %q", status.Bus, BusReconnecting)
	}
	if err := monitor.discover(context.Background()); err == nil {
		t.Error("discover read the registry through a connection that is down")
	}
	monitor.report(fmt.Errorf("reading: %w", bus.ErrUnavailable))
	monitor.report(errors.New("another failure"))
	if len(reported) != 1 || reported[0].Error() != "another failure" {
		t.Errorf("reported %v, want only the failure that is not an outage", reported)
	}
}

// failingHistory fails every read, as a transport does while the broker is gone.
type failingHistory struct{ *fakeBus }

func (failingHistory) History(context.Context, bus.Channel, uint64, int) ([]bus.Record, error) {
	return nil, errors.New("reading the history: broker lost")
}

// recordingHistory remembers the sequence each read started after.
type recordingHistory struct {
	*fakeBus
	mu     sync.Mutex
	afters []uint64
}

func (history *recordingHistory) History(ctx context.Context, from bus.Channel, after uint64, limit int) ([]bus.Record, error) {
	history.mu.Lock()
	history.afters = append(history.afters, after)
	history.mu.Unlock()
	return history.fakeBus.History(ctx, from, after, limit)
}

func TestFollowResumesAfterTheLastStoredSequence(t *testing.T) {
	fake := newFakeBus(t)
	general := bus.GeneralChannel()
	fake.store(general, 7, []byte("not an envelope"))
	history := &recordingHistory{fakeBus: fake}
	monitor := New(Config{Admin: fake, History: history, Report: func(error) {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.follow(ctx, general); close(done) }()

	waitFor := func(what string, condition func() bool) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); !condition(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitFor("the first record", func() bool { return len(monitor.Messages("general", 0, 10)) == 1 })
	fake.store(general, 9, []byte("another"))
	waitFor("the second record", func() bool { return len(monitor.Messages("general", 0, 10)) == 2 })
	cancel()
	<-done

	history.mu.Lock()
	defer history.mu.Unlock()
	if history.afters[0] != 0 {
		t.Fatalf("the first read started after %d, want 0", history.afters[0])
	}
	for _, after := range history.afters[1:] {
		if after < 7 {
			t.Errorf("a later read started after %d, want at least the stored sequence 7: %v", after, history.afters)
		}
	}
}

func TestRunReturnsPromptlyWhenCancelledWhileEveryReadFails(t *testing.T) {
	fake := newFakeBus(t)
	monitor := New(Config{Admin: fake, History: failingHistory{fake}, Report: func(error) {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond) // the followers are in their pause after the failed read
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run is still waiting for its followers")
	}
}
