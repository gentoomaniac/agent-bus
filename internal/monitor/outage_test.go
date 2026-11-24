package monitor

import (
	"context"
	"errors"
	"fmt"
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
