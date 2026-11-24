package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// requestExpiry is how long a fetch or history read waits for records before it returns what it
// has: short, so a quiet channel does not hold the caller up (design.md §4a).
const requestExpiry = time.Second

// Transport is one agent's message path to stream BUS. It implements bus.Transport.
type Transport struct {
	session    *session
	agentToken string
}

var _ bus.Transport = (*Transport)(nil)

// NewTransport connects as the agent. Replies arrive on the agent's own inbox prefix, the only
// one it may subscribe to.
func NewTransport(conn Connection, agentID string) (*Transport, error) {
	token := bus.AgentToken(agentID)
	session, err := connect(conn, "_INBOX."+token)
	if err != nil {
		return nil, err
	}
	return &Transport{session: session, agentToken: token}, nil
}

func (transport *Transport) Close() { transport.session.close() }

// Publish sets the message ID to the destination subject plus the envelope ID, so an ID reused on
// another channel is not mistaken for a duplicate. The stream drops a repeat within its window.
func (transport *Transport) Publish(ctx context.Context, to bus.Channel, id string, payload []byte) (uint64, error) {
	destination := subject(to)
	ack, err := transport.session.stream.Publish(ctx, destination, payload, jetstream.WithMsgID(destination+"/"+id))
	if err != nil {
		return 0, fmt.Errorf("publishing to %s: %w", to, transport.session.explain(err))
	}
	return ack.Sequence, nil
}

// Fetch returns the deliveries it could read, together with an error for each message it skipped.
func (transport *Transport) Fetch(ctx context.Context, from bus.Channel, max int) ([]bus.Delivery, error) {
	consumer, err := transport.session.stream.Consumer(ctx, busStream, consumerName(transport.agentToken, from))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", from, transport.session.explain(err))
	}
	batch, err := consumer.Fetch(max, jetstream.FetchMaxWait(requestExpiry))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", from, transport.session.explain(err))
	}
	var deliveries []bus.Delivery
	var skipped []error
	for message := range batch.Messages() {
		metadata, err := message.Metadata()
		if err != nil {
			// Left unacknowledged, the message comes back after the ack wait.
			skipped = append(skipped, fmt.Errorf("skipping a message of %s with unreadable metadata: %w", from, err))
			continue
		}
		deliveries = append(deliveries, bus.Delivery{
			Record: bus.Record{Sequence: metadata.Sequence.Stream, Payload: message.Data()},
			Ack:    message.DoubleAck,
		})
	}
	return deliveries, errors.Join(append(skipped, transport.session.explain(batch.Error()))...)
}

// History works only with admin credentials until busd exists (milestone 3): an agent has no
// access to the stream itself, only to its own consumers.
func (transport *Transport) History(ctx context.Context, from bus.Channel, afterSequence uint64, limit int) ([]bus.Record, error) {
	config := jetstream.OrderedConsumerConfig{FilterSubjects: []string{subject(from)}, DeliverPolicy: jetstream.DeliverAllPolicy}
	if afterSequence > 0 {
		config.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		config.OptStartSeq = afterSequence + 1
	}
	consumer, err := transport.session.stream.OrderedConsumer(ctx, busStream, config)
	if err != nil {
		return nil, fmt.Errorf("reading the history of %s: %w", from, transport.session.explain(err))
	}
	batch, err := consumer.Fetch(limit, jetstream.FetchMaxWait(requestExpiry))
	if err != nil {
		return nil, fmt.Errorf("reading the history of %s: %w", from, transport.session.explain(err))
	}
	var records []bus.Record
	for message := range batch.Messages() {
		metadata, err := message.Metadata()
		if err != nil {
			return nil, err
		}
		records = append(records, bus.Record{Sequence: metadata.Sequence.Stream, Payload: message.Data()})
	}
	return records, transport.session.explain(batch.Error())
}
