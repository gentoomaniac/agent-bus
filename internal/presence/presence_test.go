package presence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func record(t *testing.T, events ...Event) Record {
	t.Helper()
	tracker := NewTracker(start)
	for i, event := range events {
		tracker.Apply(event, start.Add(time.Duration(i+1)*time.Second))
	}
	return tracker.Record(start.Add(time.Minute), Mail{})
}

func TestEventsMapToStates(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
		want   string
	}{
		{"a new session is idle", nil, Idle},
		{"a prompt starts work", []Event{{Kind: EventPrompt}}, Working},
		{"a stop ends it", []Event{{Kind: EventPrompt}, {Kind: EventStop}}, Idle},
		{"a tool call without a prompt is work", []Event{{Kind: EventTool, Tool: "Read"}}, Working},
		{"a rate limit", []Event{{Kind: EventPrompt}, {Kind: EventFailure, Detail: "rate_limit"}}, RateLimited},
		{"another API error", []Event{{Kind: EventFailure, Detail: "server_error"}}, "error:server_error"},
		{"an error kind is cleaned", []Event{{Kind: EventFailure, Detail: "Bad Kind!\n"}}, "error:badkind"},
		{"no kind", []Event{{Kind: EventFailure}}, "error:unknown"},
		{"waiting for the human", []Event{{Kind: EventPrompt}, {Kind: EventNeedsOwner}}, WaitingForOwner},
		{"work resumes after a limit", []Event{{Kind: EventFailure, Detail: "rate_limit"}, {Kind: EventPrompt}}, Working},
		{"the session ended", []Event{{Kind: EventPrompt}, {Kind: EventSessionEnd}}, Offline},
		{"a new session after the end", []Event{{Kind: EventSessionEnd}, {Kind: EventSessionStart}}, Idle},
		{"an unknown event changes nothing", []Event{{Kind: "bogus"}}, Idle},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := record(t, test.events...).State; got != test.want {
				t.Errorf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSinceChangesOnlyWithTheState(t *testing.T) {
	tracker := NewTracker(start)
	tracker.Apply(Event{Kind: EventPrompt}, start.Add(time.Second))
	tracker.Apply(Event{Kind: EventTool, Tool: "Read"}, start.Add(5*time.Second))
	if got := tracker.Record(start.Add(time.Minute), Mail{}).Since; !got.Equal(start.Add(time.Second)) {
		t.Errorf("since = %s, want the time of the prompt", got)
	}
}

func TestToolsWorkersAndRecentActivity(t *testing.T) {
	tracker := NewTracker(start)
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	tracker.Apply(Event{Kind: EventTool, Tool: "Edit", Detail: "main.go"}, at(1))
	got := tracker.Record(at(2), Mail{})
	if got.Activity != "Edit main.go" || !got.ActivitySince.Equal(at(1)) {
		t.Errorf("activity = %q since %s", got.Activity, got.ActivitySince)
	}
	tracker.Apply(Event{Kind: EventToolDone}, at(3))
	if got := tracker.Record(at(4), Mail{}); got.Activity != "" || !got.ActivitySince.IsZero() {
		t.Errorf("after the tool: %+v", got)
	}

	tracker.Apply(Event{Kind: EventWorkerStart, Detail: "review the diff"}, at(5))
	tracker.Apply(Event{Kind: EventWorkerStart, Detail: "run tests"}, at(6))
	if got := tracker.Record(at(7), Mail{}); len(got.Workers) != 2 || got.Workers[0].Description != "review the diff" {
		t.Errorf("workers = %+v", got.Workers)
	}
	tracker.Apply(Event{Kind: EventWorkerEnd}, at(8))
	if got := tracker.Record(at(9), Mail{}); len(got.Workers) != 1 || got.Workers[0].Description != "run tests" {
		t.Errorf("workers after one ended = %+v", got.Workers)
	}
	tracker.Apply(Event{Kind: EventSessionEnd}, at(10))
	if got := tracker.Record(at(11), Mail{}); len(got.Workers) != 0 {
		t.Errorf("workers after the session ended = %+v", got.Workers)
	}

	for i := 0; i < 15; i++ {
		tracker.Apply(Event{Kind: EventTool, Tool: "Read"}, at(20+i))
	}
	if got := tracker.Record(at(50), Mail{}); len(got.Recent) != maxRecent {
		t.Errorf("%d recent entries, want %d", len(got.Recent), maxRecent)
	}
}

func TestTextIsClipped(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := record(t, Event{Kind: EventTool, Tool: "Bash", Detail: long}, Event{Kind: EventWorkerStart, Detail: "multi\nline " + long})
	if len([]rune(got.Activity)) > maxTextRunes || len([]rune(got.Workers[0].Description)) > maxTextRunes || strings.Contains(got.Workers[0].Description, "\n") {
		t.Errorf("not clipped: %q, %q", got.Activity, got.Workers[0].Description)
	}
}

func TestIdleAgentWithUnseenMailBecomesUnresponsive(t *testing.T) {
	tracker := NewTracker(start)
	arrived := start.Add(time.Minute)
	mail := Mail{Pending: 1, OldestUnseen: arrived}
	if got := tracker.Record(arrived.Add(UnresponsiveAfter-time.Second), mail); got.State != Idle || got.Pending != 1 {
		t.Errorf("before the limit: %+v", got)
	}
	got := tracker.Record(arrived.Add(UnresponsiveAfter), mail)
	if got.State != Unresponsive || !got.Since.Equal(arrived.Add(UnresponsiveAfter)) {
		t.Errorf("after the limit: %+v", got)
	}
	tracker.Apply(Event{Kind: EventPrompt}, arrived)
	if got := tracker.Record(arrived.Add(time.Hour), mail); got.State != Working {
		t.Errorf("a working agent is busy, not unresponsive: %+v", got)
	}
}

func TestReadersCallAnAgentOfflineWithoutHeartbeat(t *testing.T) {
	published := Record{State: Working, Updated: start}
	if got := published.StateAt(start.Add(OfflineAfter)); got != Working {
		t.Errorf("at the limit = %q", got)
	}
	if got := published.StateAt(start.Add(OfflineAfter + time.Second)); got != Offline {
		t.Errorf("past the limit = %q", got)
	}
	if got := (Record{State: Offline, Updated: start}).StateAt(start); got != Offline {
		t.Errorf("an explicit offline = %q", got)
	}
}

type fakeSink struct {
	published []Record
	mail      Mail
	err       error
}

func (sink *fakeSink) PublishPresence(_ context.Context, record Record) error {
	if sink.err != nil {
		return sink.err
	}
	sink.published = append(sink.published, record)
	return nil
}

func (sink *fakeSink) MailWaiting() Mail { return sink.mail }

func TestPublisherThrottlesChangesAndHeartbeats(t *testing.T) {
	now := start
	sink := &fakeSink{}
	publisher := NewPublisher(sink, 30*time.Second, func() time.Time { return now })
	step := func(after time.Duration) bool {
		now = now.Add(after)
		return publisher.Step(context.Background())
	}

	if !step(0) {
		t.Fatal("the first step must publish")
	}
	if step(time.Second) {
		t.Error("nothing changed, nothing to publish")
	}
	publisher.Event(Event{Kind: EventPrompt})
	if step(time.Second) {
		t.Error("a change inside the throttle window was published")
	}
	if !step(time.Second) {
		t.Error("the change was not published once the window passed")
	}
	if last := sink.published[len(sink.published)-1]; last.State != Working || !last.Updated.Equal(now) {
		t.Errorf("published %+v", last)
	}
	if step(20 * time.Second) {
		t.Error("published before the heartbeat")
	}
	if !step(10 * time.Second) {
		t.Error("no heartbeat after 30 seconds")
	}
	if len(sink.published) != 3 {
		t.Errorf("%d publishes, want 3", len(sink.published))
	}
}

func TestPublisherRetriesAFailedPublishAndSeesWaitingMail(t *testing.T) {
	now := start
	sink := &fakeSink{err: errors.New("bus down")}
	publisher := NewPublisher(sink, 30*time.Second, func() time.Time { return now })
	if publisher.Step(context.Background()) {
		t.Fatal("published while the bus was down")
	}
	sink.err = nil
	now = now.Add(time.Second)
	if !publisher.Step(context.Background()) {
		t.Error("the failed publish was not retried")
	}
	sink.mail = Mail{Pending: 2}
	now = now.Add(MinPublishInterval)
	if !publisher.Step(context.Background()) || sink.published[len(sink.published)-1].Pending != 2 {
		t.Error("a change in waiting mail was not published")
	}
}

func TestRunPublishesOfflineWhenItEnds(t *testing.T) {
	sink := &fakeSink{}
	publisher := NewPublisher(sink, time.Hour, time.Now)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { publisher.Run(ctx); close(done) }()
	stop()
	<-done
	if len(sink.published) == 0 || sink.published[len(sink.published)-1].State != Offline {
		t.Errorf("published %+v, want a last record that is offline", sink.published)
	}
}
