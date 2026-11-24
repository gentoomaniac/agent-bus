package monitor

import (
	"context"
	"testing"
	"time"
)

func TestTheMonitorsOwnIdentityShowsAsMonitor(t *testing.T) {
	fake := newFakeBus(t, newTestAgent("monitor@alice"))
	monitor := New(Config{Admin: fake, History: fake, Sender: &fakeSender{}, Report: func(err error) { t.Error(err) }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, view := range monitor.Presences() {
			if view.Agent == "monitor@alice" {
				if view.State != "monitor" {
					t.Errorf("own identity: %+v", view)
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the monitor's own identity is not listed")
}
