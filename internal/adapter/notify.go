package adapter

import "time"

// notifyCooldown is the least time between two notifications, so two agents cannot keep waking
// each other (design.md §8 rule 3).
const notifyCooldown = 10 * time.Second

// notifications remembers which buffered messages the delivery hooks already told the agent about.
// Only mail that targets the agent is ever told: the rest sits in the inbox silently (design.md §7).
type notifications struct {
	notified map[string]bool // by message ID
	lastAt   time.Time
}

// NewMail returns the buffered messages that target the agent and that it was not told about yet,
// and marks them told, plus the total number of unread messages. During the cooldown after the last notification it returns
// no new mail and leaves it un-notified for the next call.
func (adapter *Adapter) NewMail() (fresh []Message, unread int, err error) {
	if err := adapter.Ready(); err != nil {
		return nil, 0, err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	now := adapter.config.Now()
	if now.Sub(adapter.notifications.lastAt) >= notifyCooldown {
		fresh = adapter.markNotified(false)
		if len(fresh) > 0 {
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
	return adapter.markNotified(true), nil
}

// markNotified marks the buffered messages that target the agent told and returns the new ones, or
// all of them when includeTold is set. The caller holds mu.
func (adapter *Adapter) markNotified(includeTold bool) []Message {
	if adapter.notifications.notified == nil {
		adapter.notifications.notified = map[string]bool{}
	}
	var listed []Message
	for _, message := range adapter.inbox.messages {
		if !message.TargetsMe {
			continue
		}
		id := message.Envelope.ID
		if includeTold || !adapter.notifications.notified[id] {
			listed = append(listed, *message)
		}
		adapter.notifications.notified[id] = true
	}
	return listed
}
