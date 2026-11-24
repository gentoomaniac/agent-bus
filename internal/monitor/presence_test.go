package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

const presenceAgent = "api-client@alice"

func runPresenceMonitor(t *testing.T, fake *fakeBus, offlineAfter time.Duration) *Monitor {
	t.Helper()
	monitor := New(Config{Admin: fake, History: fake, OfflineAfter: offlineAfter, Report: func(err error) { t.Error(err) }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return monitor
}

func recordJSON(t *testing.T, record presence.Record) []byte {
	t.Helper()
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func working(updated time.Time) presence.Record {
	return presence.Record{
		State: presence.Working, Since: updated, Updated: updated, Activity: "Edit main.go", ActivitySince: updated,
		Workers: []presence.Worker{{Description: "review", Since: updated}}, Pending: 2,
	}
}

// waitForPresence polls the monitor's view of the agent until it satisfies the condition.
func waitForPresence(t *testing.T, monitor *Monitor, condition func(Presence) bool) Presence {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, view := range monitor.Presences() {
			if view.Agent == presenceAgent && condition(view) {
				return view
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("presence never matched; views: %+v", monitor.Presences())
	return Presence{}
}

func TestPresenceIsOfflineUntilTheAgentPublishes(t *testing.T) {
	fake := newFakeBus(t, newTestAgent(presenceAgent))
	monitor := runPresenceMonitor(t, fake, 0)
	view := waitForPresence(t, monitor, func(Presence) bool { return true })
	if view.State != presence.Offline || view.Activity != "" {
		t.Errorf("never published: %+v", view)
	}
}

func TestPresenceShowsAPublishedRecordAndOneStoredBeforeStart(t *testing.T) {
	fake := newFakeBus(t, newTestAgent(presenceAgent))
	fake.presence[presenceAgent] = recordJSON(t, working(time.Now()))
	monitor := runPresenceMonitor(t, fake, 0)

	view := waitForPresence(t, monitor, func(view Presence) bool { return view.State == presence.Working })
	if view.Activity != "Edit main.go" || len(view.Workers) != 1 || view.Pending != 2 {
		t.Errorf("record stored before the start: %+v", view)
	}

	fake.publishPresence(presenceAgent, recordJSON(t, presence.Record{State: presence.RateLimited, Since: time.Now(), Updated: time.Now()}))
	view = waitForPresence(t, monitor, func(view Presence) bool { return view.State == presence.RateLimited })
	if view.Activity != "" {
		t.Errorf("after the rate limit: %+v", view)
	}
}

func TestPresenceTurnsOfflineWhenTheHeartbeatStopsAndSaysSo(t *testing.T) {
	fake := newFakeBus(t, newTestAgent(presenceAgent))
	monitor := runPresenceMonitor(t, fake, 300*time.Millisecond)
	waitForPresence(t, monitor, func(Presence) bool { return true })
	views, unsubscribe := monitor.SubscribePresence()
	defer unsubscribe()

	fake.publishPresence(presenceAgent, recordJSON(t, working(time.Now())))
	if view := <-views; view.State != presence.Working {
		t.Fatalf("first view: %+v", view)
	}
	select {
	case view := <-views:
		if view.State != presence.Offline || view.Activity != "" {
			t.Errorf("after the heartbeat ran out: %+v", view)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no offline view after the heartbeat stopped")
	}
}

func TestPresenceOfAnExplicitOfflineIsShownAtOnce(t *testing.T) {
	fake := newFakeBus(t, newTestAgent(presenceAgent))
	monitor := runPresenceMonitor(t, fake, 0)
	fake.publishPresence(presenceAgent, recordJSON(t, working(time.Now())))
	waitForPresence(t, monitor, func(view Presence) bool { return view.State == presence.Working })
	fake.publishPresence(presenceAgent, recordJSON(t, presence.Record{State: presence.Offline, Since: time.Now(), Updated: time.Now()}))
	waitForPresence(t, monitor, func(view Presence) bool { return view.State == presence.Offline })
}

func TestAnOlderRecordDoesNotReplaceANewerOne(t *testing.T) {
	monitor := New(Config{})
	now := time.Now()
	monitor.receivePresence(presenceAgent, recordJSON(t, working(now)))
	monitor.receivePresence(presenceAgent, recordJSON(t, presence.Record{State: presence.Idle, Updated: now.Add(-time.Minute)}))
	if got := monitor.records[presenceAgent].State; got != presence.Working {
		t.Errorf("state = %q", got)
	}
	monitor.receivePresence(presenceAgent, []byte("not json"))
}

func TestPresenceIsServedAndStreamed(t *testing.T) {
	fake := newFakeBus(t, newTestAgent(presenceAgent))
	monitor := runPresenceMonitor(t, fake, 0)
	server := serve(monitor)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewScanner(response.Body)
	lines.Scan() // ": connected"

	fake.publishPresence(presenceAgent, recordJSON(t, working(time.Now())))
	found := make(chan Presence, 1)
	go func() {
		isPresence := false
		for lines.Scan() {
			line := lines.Text()
			isPresence = isPresence || line == "event: presence"
			if data, isData := strings.CutPrefix(line, "data: "); isData && isPresence {
				var view Presence
				if json.Unmarshal([]byte(data), &view) == nil {
					found <- view
					return
				}
			}
		}
	}()
	select {
	case view := <-found:
		if view.Agent != presenceAgent || view.State != presence.Working {
			t.Errorf("event = %+v", view)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no presence event arrived")
	}

	var views []Presence
	getJSON(t, server, "/api/presence", &views)
	if len(views) != 1 || views[0].State != presence.Working {
		t.Errorf("/api/presence = %+v", views)
	}
}
