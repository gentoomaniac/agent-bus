package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
)

// Requester sends requests to busd and waits for the answers: what `agent-bus enroll` does as the
// shared enrollment login, and `agent-busctl ca` as the admin.
type Requester struct{ session *session }

// NewRequester connects as conn says. Answers arrive on inboxPrefix, which the login must be
// allowed to subscribe to (enroll.InboxPrefix for the enrollment login).
func NewRequester(conn Connection, inboxPrefix string) (*Requester, error) {
	session, err := connect(conn, inboxPrefix)
	if err != nil {
		return nil, err
	}
	return &Requester{session: session}, nil
}

func (requester *Requester) Close() { requester.session.close() }

// Request publishes body on subject and returns busd's reply.
func (requester *Requester) Request(ctx context.Context, subject string, body []byte) ([]byte, error) {
	if err := requester.session.unavailable(); err != nil {
		return nil, err
	}
	reply, err := requester.session.conn.RequestWithContext(ctx, subject, body)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return nil, errors.New("busd is not running on the broker (it starts with the broker container when ca.key exists; see docs/podman.md)")
	case err != nil:
		return nil, fmt.Errorf("asking busd on %s: %w", subject, requester.session.explain(err))
	}
	return reply.Data, nil
}
