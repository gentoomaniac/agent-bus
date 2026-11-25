package hook

import (
	"errors"
	"net"
	"testing"
	"time"
)

// flakyListener fails its first accepts with a transient error, hands out one connection, and
// reports itself closed once that connection was served.
type flakyListener struct {
	net.Listener
	failures   int
	connection net.Conn
	served     chan struct{}
}

func (listener *flakyListener) Accept() (net.Conn, error) {
	switch {
	case listener.failures > 0:
		listener.failures--
		return nil, errors.New("accept: too many open files")
	case listener.connection != nil:
		connection := listener.connection
		listener.connection = nil
		return connection, nil
	}
	<-listener.served
	return nil, net.ErrClosed
}

func TestAcceptLoopSurvivesATransientError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	listener := &flakyListener{failures: 1, connection: server, served: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		acceptLoop(listener, func(net.Conn) { close(listener.served) })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(acceptRetryDelay + 5*time.Second):
		t.Fatal("the loop gave up after the failed accept, or did not stop when the listener closed")
	}
}
