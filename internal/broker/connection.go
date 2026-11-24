package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// connectTimeout bounds how long connecting (TCP, TLS and authentication) may take, so a broker
// that is down ends in an error instead of a hang.
const connectTimeout = 10 * time.Second

// Connection says how to reach the broker and who to be.
type Connection struct {
	Address    string
	CACertPath string
	User       string
	Password   string
}

// session is an authenticated connection with the JetStream API on top. NATS answers a call the
// server refused only with silence and reports the reason as an asynchronous error on the
// connection, so the session remembers the last one to explain a timeout (design.md §4a).
type session struct {
	conn   *nats.Conn
	stream jetstream.JetStream

	mu              sync.Mutex
	lastServerError string
}

// connect opens the session over TLS, trusting only the CA certificate. An inboxPrefix, if not
// empty, is where replies to this connection's requests arrive; agents may subscribe only there.
func connect(conn Connection, inboxPrefix string) (*session, error) {
	caPEM, err := os.ReadFile(conn.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s contains no certificate", conn.CACertPath)
	}

	s := &session{}
	options := []nats.Option{
		nats.UserInfo(conn.User, conn.Password),
		nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		nats.Timeout(connectTimeout),
		nats.NoReconnect(),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { s.rememberServerError(err) }),
	}
	if inboxPrefix != "" {
		options = append(options, nats.CustomInboxPrefix(inboxPrefix))
	}
	s.conn, err = nats.Connect(conn.Address, options...)
	if err != nil {
		return nil, fmt.Errorf("connecting to the broker as %s: %w", conn.User, err)
	}
	s.stream, err = jetstream.New(s.conn)
	if err != nil {
		s.conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *session) close() { s.conn.Close() }

func (s *session) rememberServerError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastServerError = err.Error()
}

// explain adds the server's reason, such as a permissions violation, to a timeout.
func (s *session) explain(err error) error {
	if err == nil || !isTimeout(err) {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastServerError == "" {
		return err
	}
	return fmt.Errorf("%w (the server reported: %s)", err, s.lastServerError)
}

func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, jetstream.ErrNoStreamResponse)
}
