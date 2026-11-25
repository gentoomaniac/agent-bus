package adapter

import (
	"context"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// Default bounds of the inbox buffer (docs/design/delivery.md): the message limit equals the consumers'
// MaxAckPending, so the broker never has more unacknowledged mail for us than we can hold.
const (
	DefaultInboxMessages = 500
	DefaultInboxBytes    = 8 << 20
	// DefaultContextMessages caps the mail that does not target the agent (--deliver=all), so
	// context can never keep targeted mail from being fetched.
	DefaultContextMessages = 100
)

// StaleAfter is how old a message must be to count as context only: the agent is told not to act on
// it (the MCP instructions name it "older than a day"), and the delivery hooks do not announce it.
// A message waits that long when the agent was offline, or after a bus outage.
const StaleAfter = 24 * time.Hour

// Message is a verified message waiting for the agent.
type Message struct {
	Envelope     envelope.Envelope
	Channel      bus.Channel
	Sequence     uint64
	FirstContact bool // the sender's key was pinned because of this message
	TargetsMe    bool // the message needs this agent's response (docs/design/protocol.md)
	Acked        bool // the agent sent an ack reply to it; it stays unread until answered
	Digest       string
	ArrivedAt    time.Time

	// returned is set once Inbox listed the message: wait_for_messages then skips it, so an agent
	// idling with mail it has not answered does not spin.
	returned bool

	delivery bus.Delivery
	size     int
}

// Age is the time since the sender wrote the message, by its signed timestamp: the time it
// reached this agent says nothing when the agent was offline. A sender whose clock is ahead has age 0.
func (message Message) Age(now time.Time) time.Duration {
	return max(now.Sub(message.Envelope.TS), 0)
}

// inbox holds verified, unacknowledged messages in arrival order. Mail that targets the agent is
// bounded by count and bytes and leaves only when the agent acknowledges it. Context (mail that
// does not target the agent) has a count cap of its own: the adapter drops the oldest to make room.
type inbox struct {
	maxMessages, maxBytes, maxContext int
	messages                          []*Message
	bytes                             int
}

// fetchBatch is how many records the reader may fetch now, counting targeted mail only: context
// makes room for itself. Records are not known in size before they arrive, so the byte room
// counts each as the largest a record can be; targeted mail then never exceeds its byte bound.
func (buffer *inbox) fetchBatch() int {
	targeted, targetedBytes := 0, 0
	for _, message := range buffer.messages {
		if message.TargetsMe {
			targeted++
			targetedBytes += message.size
		}
	}
	room := min(buffer.maxMessages-targeted, (buffer.maxBytes-targetedBytes)/envelope.MaxRecordBytes)
	return max(room, 0)
}

// contextFull returns the oldest buffered context message when the context cap is reached, else nil.
func (buffer *inbox) contextFull() *Message {
	var oldest *Message
	count := 0
	for _, message := range buffer.messages {
		if !message.TargetsMe {
			if oldest == nil {
				oldest = message
			}
			count++
		}
	}
	if count < buffer.maxContext {
		return nil
	}
	return oldest
}

func (buffer *inbox) add(message *Message) {
	buffer.messages = append(buffer.messages, message)
	buffer.bytes += message.size
}

// find returns the buffered message with the same destination, verified sender and ID.
func (buffer *inbox) find(channel bus.Channel, from, id string) *Message {
	for _, message := range buffer.messages {
		if message.Channel == channel && message.Envelope.From == from && message.Envelope.ID == id {
			return message
		}
	}
	return nil
}

// findByID returns the first buffered message with the ID.
func (buffer *inbox) findByID(id string) *Message {
	for _, message := range buffer.messages {
		if message.Envelope.ID == id {
			return message
		}
	}
	return nil
}

func (buffer *inbox) remove(target *Message) {
	for i, message := range buffer.messages {
		if message == target {
			buffer.messages = append(buffer.messages[:i], buffer.messages[i+1:]...)
			buffer.bytes -= message.size
			return
		}
	}
}

// release hands every buffered record back to the broker unhandled, so the next session gets it at
// once instead of after the ack wait.
func (buffer *inbox) release(ctx context.Context) {
	for _, message := range buffer.messages {
		if message.delivery.Release != nil {
			_ = message.delivery.Release(ctx)
		}
	}
}

// keepAlive tells the broker that every buffered message is still being worked on, so it does not
// redeliver one just because the agent reads slowly. It returns the first error and goes on.
func (buffer *inbox) keepAlive(ctx context.Context) error {
	var first error
	for _, message := range buffer.messages {
		if err := message.delivery.InProgress(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
