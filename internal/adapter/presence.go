package adapter

import (
	"context"
	"encoding/json"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

// PublishPresence puts the agent's own presence record on the bus.
func (adapter *Adapter) PublishPresence(_ context.Context, record presence.Record) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return adapter.config.Transport.PublishPresence(payload)
}

// Presence reads another agent's last record. ok is false when it never published one.
func (adapter *Adapter) Presence(ctx context.Context, agentID string) (record presence.Record, ok bool, err error) {
	payload, err := adapter.config.Transport.Presence(ctx, agentID)
	if err != nil || payload == nil {
		return record, false, err
	}
	if err := json.Unmarshal(payload, &record); err != nil {
		return record, false, fmt.Errorf("presence of %s: %w", agentID, err)
	}
	return record, true, nil
}

// MailWaiting tells the presence publisher how much targeted mail waits and since when the oldest
// piece has gone unseen: no tool call has listed it, so the agent has not reacted to it. An ack
// counts as waiting but never as unseen: no hook announces it, so nothing would make the agent look.
func (adapter *Adapter) MailWaiting() presence.Mail {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	var mail presence.Mail
	for _, message := range adapter.inbox.messages {
		if !message.TargetsMe {
			continue
		}
		mail.Pending++
		if !message.returned && !asksNothing(message) && (mail.OldestUnseen.IsZero() || message.ArrivedAt.Before(mail.OldestUnseen)) {
			mail.OldestUnseen = message.ArrivedAt
		}
	}
	return mail
}
