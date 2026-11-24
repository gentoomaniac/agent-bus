package monitor

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
