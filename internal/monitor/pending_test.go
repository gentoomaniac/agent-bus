package monitor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatusCountsTheEnrollmentsBusdAnnounces(t *testing.T) {
	fake := newFakeBus(t)
	monitor := runPresenceMonitor(t, fake, 0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		fake.mu.Lock()
		watching := len(fake.pendingWatchers) > 0
		fake.mu.Unlock()
		if watching {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the monitor never started watching the pending count")
		}
		time.Sleep(time.Millisecond)
	}

	if got := monitor.Status().PendingEnrollments; got != 0 {
		t.Errorf("before any announcement: %d", got)
	}
	fake.announcePending(2)
	if got := monitor.Status().PendingEnrollments; got != 2 {
		t.Errorf("after announcing 2: %d", got)
	}
	if body := statusJSON(t, monitor); body["pending_enrollments"] != float64(2) {
		t.Errorf("status JSON %v", body)
	}

	monitor.pendingMu.Lock()
	monitor.pendingAt = time.Now().Add(-2 * pendingFresh)
	monitor.pendingMu.Unlock()
	if got := monitor.Status().PendingEnrollments; got != 0 {
		t.Errorf("a count busd stopped repeating still shows: %d", got)
	}
	fake.announcePending(0)
	if _, shown := statusJSON(t, monitor)["pending_enrollments"]; shown {
		t.Error("the field is present with nothing pending")
	}
}

func statusJSON(t *testing.T, monitor *Monitor) map[string]any {
	t.Helper()
	body, err := json.Marshal(monitor.Status())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
