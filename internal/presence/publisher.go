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

	mu          sync.Mutex
	tracker     *Tracker
	lastContent string // of the last record published, without its Updated time
	lastAt      time.Time
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
	if publisher.sink.PublishPresence(ctx, record) != nil {
		return false
	}
	publisher.lastContent, publisher.lastAt = contentOf(record), record.Updated
	return true
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
			publisher.publish(final, publisher.tracker.Record(now, Mail{}))
			return
		case <-ticker.C:
			publisher.Step(ctx)
		}
	}
}
