package adapter

import (
	"context"
	"time"
)

// notifyCooldown is the least time between two notifications, so two agents cannot keep waking
// each other (docs/design/safety.md rule 3).
const notifyCooldown = 10 * time.Second

// notifications remembers which buffered messages the delivery hooks already told the agent about.
// Only mail that targets the agent is ever told: the rest sits in the inbox silently (docs/design/delivery.md).
type notifications struct {
	notified map[string]bool // by message ID
	lastAt   time.Time
}

// NewMail returns the buffered messages that target the agent and that it was not told about yet,
// and marks them told, plus the total number of unread messages. During the cooldown after the last notification it returns
// no new mail and leaves it un-notified for the next call.
func (adapter *Adapter) NewMail() (fresh []Message, unread int, err error) {
	return adapter.newMail(true)
}

// PeekNewMail is NewMail without marking anything told, for a caller that marks (MarkNotified)
// only once it has delivered the notice.
func (adapter *Adapter) PeekNewMail() (fresh []Message, unread int, err error) {
	return adapter.newMail(false)
}

// WaitNewMail is NewMail that blocks until there is mail to return: targeted mail not told yet,
// once the cooldown since the last notification is over. It marks the mail told like NewMail. It
// returns the context's error when ctx ends first.
func (adapter *Adapter) WaitNewMail(ctx context.Context) (fresh []Message, unread int, err error) {
	if err := adapter.Ready(); err != nil {
		return nil, 0, err
	}
	for {
		adapter.mu.Lock()
		if adapter.arrival == nil {
			adapter.arrival = make(chan struct{})
		}
		arrival := adapter.arrival
		var cooldownOver <-chan time.Time
		now := adapter.config.Now()
		if wait := notifyCooldown - now.Sub(adapter.notifications.lastAt); wait <= 0 {
			if fresh = adapter.markNotified(false, true); len(fresh) > 0 {
				adapter.notifications.lastAt = now
				unread = len(adapter.inbox.messages)
				adapter.mu.Unlock()
				return fresh, unread, nil
			}
		} else if len(adapter.markNotified(false, false)) > 0 {
			cooldownOver = time.After(wait)
		}
		adapter.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-arrival:
		case <-cooldownOver:
		}
	}
}

// MarkNotified marks the messages told and starts the cooldown, like NewMail would have.
func (adapter *Adapter) MarkNotified(ids []string) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.notifications.notified == nil {
		adapter.notifications.notified = map[string]bool{}
	}
	for _, id := range ids {
		adapter.notifications.notified[id] = true
	}
	adapter.notifications.lastAt = adapter.config.Now()
}

func (adapter *Adapter) newMail(mark bool) (fresh []Message, unread int, err error) {
	if err := adapter.Ready(); err != nil {
		return nil, 0, err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	now := adapter.config.Now()
	if now.Sub(adapter.notifications.lastAt) >= notifyCooldown {
		fresh = adapter.markNotified(false, mark)
		if mark && len(fresh) > 0 {
			adapter.notifications.lastAt = now
		}
	}
	return fresh, len(adapter.inbox.messages), nil
}

// UnreadSummary returns every buffered message that targets the agent and marks them all told, for the summary at the
// start of a session. It ignores the cooldown and does not start one.
func (adapter *Adapter) UnreadSummary() ([]Message, error) {
	if err := adapter.Ready(); err != nil {
		return nil, err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.markNotified(true, true), nil
}

// markNotified marks the buffered messages that target the agent told and returns the new ones, or
// all of them when includeTold is set. The caller holds mu.
func (adapter *Adapter) markNotified(includeTold, mark bool) []Message {
	if adapter.notifications.notified == nil {
		adapter.notifications.notified = map[string]bool{}
	}
	var listed []Message
	for _, message := range adapter.inbox.messages {
		if !message.TargetsMe {
			continue
		}
		id := message.Envelope.ID
		// Mail the agent already read, listed by Inbox (the returned flag), counts as told: an acknowledged request stays unread
		// until it is answered, and announcing it again as new would send the agent back to it.
		if includeTold || (!adapter.notifications.notified[id] && !message.returned) {
			listed = append(listed, *message)
		}
		if mark {
			adapter.notifications.notified[id] = true
		}
	}
	return listed
}
