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

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
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

	// Reconnect makes the connection outlive the broker: it connects, and reconnects after an
	// outage, in the background on this schedule and never gives up, so it can be made while the
	// broker is down. Without it a connection fails at once, and stays closed once it is lost: what
	// a one-shot command wants.
	Reconnect *Backoff
	// Log tells of an outage and of the recovery, once each. Used only with Reconnect.
	Log func(format string, args ...any)
}

// session is an authenticated connection with the JetStream API on top. NATS answers a call the
// server refused only with silence and reports the reason as an asynchronous error on the
// connection, so the session remembers the last one to explain a timeout (design.md §4a).
type session struct {
	conn   *nats.Conn
	stream jetstream.JetStream

	mu              sync.Mutex
	lastServerError string
	nextAttempt     time.Time // of the next reconnect attempt
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

	if conn.Log == nil {
		conn.Log = func(string, ...any) {}
	}
	s := &session{}
	options := []nats.Option{
		nats.UserInfo(conn.User, conn.Password),
		nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		nats.Timeout(connectTimeout),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { s.rememberServerError(err) }),
	}
	if conn.Reconnect == nil {
		options = append(options, nats.NoReconnect())
	} else {
		options = append(options, s.reconnectOptions(conn)...)
	}
	if inboxPrefix != "" {
		options = append(options, nats.CustomInboxPrefix(inboxPrefix))
	}
	s.conn, err = nats.Connect(conn.Address, options...)
	if err != nil {
		return nil, fmt.Errorf("connecting to the broker as %s: %w", conn.User, err)
	}
	if conn.Reconnect != nil && s.conn.Status() == nats.RECONNECTING {
		conn.Log("the bus at %s is unreachable, retrying in the background", conn.Address)
	}
	s.stream, err = jetstream.New(s.conn)
	if err != nil {
		s.conn.Close()
		return nil, err
	}
	return s, nil
}

// reconnectOptions makes the connection retry forever, also when the first attempt fails. nats.go
// resends the subscriptions after a reconnect, and each fetch or request starts afresh, so
// reading resumes without help.
func (s *session) reconnectOptions(conn Connection) []nats.Option {
	return []nats.Option{
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.CustomReconnectDelay(func(attempt int) time.Duration {
			delay := conn.Reconnect.Delay(attempt)
			s.mu.Lock()
			s.nextAttempt = time.Now().Add(delay)
			s.mu.Unlock()
			return delay
		}),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if !nc.IsClosed() { // closing the connection ourselves is no outage
				conn.Log("lost the connection to the bus at %s (%v), reconnecting", conn.Address, err)
			}
		}),
		nats.ConnectHandler(func(*nats.Conn) { conn.Log("connected to the bus at %s", conn.Address) }),
		nats.ReconnectHandler(func(*nats.Conn) { conn.Log("reconnected to the bus at %s", conn.Address) }),
	}
}

// unavailable is bus.ErrUnavailable with the time of the next attempt while the connection is
// being re-established, and nil otherwise.
func (s *session) unavailable() error {
	if s.conn.Status() != nats.RECONNECTING {
		return nil
	}
	s.mu.Lock()
	wait := max(0, time.Until(s.nextAttempt))
	s.mu.Unlock()
	return fmt.Errorf("%w (next attempt in %s)", bus.ErrUnavailable, wait.Round(time.Second))
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
