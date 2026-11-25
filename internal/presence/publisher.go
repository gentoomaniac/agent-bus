package presence

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const (
	stepEvery    = time.Second // how often Run looks for something to publish
	finalTimeout = 2 * time.Second
)

// Sink is where a Publisher puts the record and where it learns about waiting mail.
type Sink interface {
	PublishPresence(ctx context.Context, record Record) error
	MailWaiting() Mail
}

// Publisher publishes the record of one session when it changes, at most every
// MinPublishInterval, and as a heartbeat when it does not.
type Publisher struct {
	sink      Sink
	heartbeat time.Duration
	now       func() time.Time
	// Log, when set, is told when publishing starts failing and when it works again: a failed
	// heartbeat is otherwise silent, and the owner only sees the agent go offline.
	Log func(format string, args ...any)

	mu          sync.Mutex
	tracker     *Tracker
	lastContent string // of the last record published, without its Updated time
	lastAt      time.Time
	failingFrom time.Time // when the current run of failed publishes began; zero while they work
}

func NewPublisher(sink Sink, heartbeat time.Duration, now func() time.Time) *Publisher {
	return &Publisher{sink: sink, heartbeat: heartbeat, now: now, tracker: NewTracker(now())}
}

// Event records what a hook reported. It publishes nothing itself: the next Step does.
func (publisher *Publisher) Event(event Event) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.tracker.Apply(event, publisher.now())
}

// Step publishes when the record changed and the last publish is old enough, or when the heartbeat
// is due. It says whether it published; a failed publish is tried again at the next step.
func (publisher *Publisher) Step(ctx context.Context) bool {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	now := publisher.now()
	record := publisher.tracker.Record(now, publisher.sink.MailWaiting())
	changed := contentOf(record) != publisher.lastContent
	sinceLast := now.Sub(publisher.lastAt)
	if !(changed && sinceLast >= MinPublishInterval) && sinceLast < publisher.heartbeat {
		return false
	}
	return publisher.publish(ctx, record)
}

// publish the record. The caller holds mu.
func (publisher *Publisher) publish(ctx context.Context, record Record) bool {
	if err := publisher.sink.PublishPresence(ctx, record); err != nil {
		if publisher.failingFrom.IsZero() {
			publisher.failingFrom = publisher.now()
			publisher.log("presence: publishing failed, the monitor will show this agent offline until it works again: %v", err)
		}
		return false
	}
	if !publisher.failingFrom.IsZero() {
		publisher.log("presence: publishing works again after %s", publisher.now().Sub(publisher.failingFrom).Round(time.Second))
		publisher.failingFrom = time.Time{}
	}
	publisher.lastContent, publisher.lastAt = contentOf(record), record.Updated
	return true
}

func (publisher *Publisher) log(format string, args ...any) {
	if publisher.Log != nil {
		publisher.Log(format, args...)
	}
}

// contentOf is the record without its heartbeat time: what a change is measured on.
func contentOf(record Record) string {
	record.Updated = time.Time{}
	content, _ := json.Marshal(record) // plain fields cannot fail
	return string(content)
}

// Run steps every second until ctx ends, then publishes offline once.
func (publisher *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(stepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalTimeout)
			defer cancel()
			publisher.mu.Lock()
			defer publisher.mu.Unlock()
			now := publisher.now()
			publisher.tracker.Apply(Event{Kind: EventSessionEnd}, now)
			// Straight to the sink: an outage or recovery logged at exit would only mislead.
			_ = publisher.sink.PublishPresence(final, publisher.tracker.Record(now, Mail{}))
			return
		case <-ticker.C:
			publisher.Step(ctx)
		}
	}
}
