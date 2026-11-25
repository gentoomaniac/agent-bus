//go:build integration

package itest

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"github.com/nats-io/nats.go"
)

// switchToCert gives the agent a certificate and lets the broker accept it, as the owner does.
func (h *harness) switchToCert(t *testing.T, agentID string) {
	t.Helper()
	h.run(t, h.agentBusctl, "--direct", "cert", "issue", agentID)
	h.run(t, h.agentBusctl, "--direct", "cert", "switch", agentID)
}

// mappedAddress is the wss:// address of the certificate listener.
func (h *harness) mappedAddress() string {
	return fmt.Sprintf("wss://localhost:%d", h.mappedPort)
}

// connectMapped is a raw connection to the certificate listener, with the given client
// certificate pair (empty paths: none).
func (h *harness) connectMapped(t *testing.T, certPath, keyPath string) (*nats.Conn, error) {
	t.Helper()
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(h.caCert)
	if err != nil {
		t.Fatal(err)
	}
	pool.AppendCertsFromPEM(caPEM)
	config := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if certPath != "" {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	conn, err := nats.Connect(h.mappedAddress(), nats.Secure(config), nats.NoReconnect(), nats.Timeout(5*time.Second))
	if err == nil {
		t.Cleanup(conn.Close)
	}
	return conn, err
}

func TestCertificateLogin(t *testing.T) {
	h := startBus(t)
	h.switchToCert(t, alice)
	ctx := timeout(t)

	t.Run("an agent with a certificate exchanges DMs with a password agent", func(t *testing.T) {
		aliceSession, _ := h.startAdapter(t, alice)
		bobSession, _ := h.startAdapter(t, bob)
		if !creds.HasClientCert(creds.AgentDir(h.configDir, alice)) || creds.HasClientCert(creds.AgentDir(h.configDir, bob)) {
			t.Fatal("alice should have a certificate and bob none")
		}
		if _, err := aliceSession.Send(ctx, bob, say("hello bob, by certificate")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, bobSession, 1); got[0].Envelope.Body.Text != "hello bob, by certificate" {
			t.Errorf("bob got %v", summaries(got))
		}
		if _, err := bobSession.Send(ctx, alice, say("hello alice, by password")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, aliceSession, 1); got[0].Envelope.Body.Text != "hello alice, by password" {
			t.Errorf("alice got %v", summaries(got))
		}
	})

	t.Run("the mapped port refuses a client without a certificate", func(t *testing.T) {
		password, _ := creds.ReadPassword(creds.AgentDir(h.configDir, bob))
		pool := x509.NewCertPool()
		caPEM, _ := os.ReadFile(h.caCert)
		pool.AppendCertsFromPEM(caPEM)
		conn, err := nats.Connect(h.mappedAddress(), nats.UserInfo(bus.AgentToken(bob), password),
			nats.Secure(&tls.Config{RootCAs: pool}), nats.NoReconnect(), nats.Timeout(5*time.Second))
		if err == nil {
			conn.Close()
			t.Error("a password login on the mapped port succeeded")
		}
		if conn, err := h.connectMapped(t, "", ""); err == nil {
			conn.Close()
			t.Error("a connection without a certificate was accepted")
		}
	})

	t.Run("the password listener no longer knows a certificate agent's password", func(t *testing.T) {
		password, _ := creds.ReadPassword(creds.AgentDir(h.configDir, alice))
		connection := broker.Connection{Address: h.address, CACertPath: h.caCert, User: bus.AgentToken(alice), Password: password}
		if transport, err := broker.NewTransport(connection, alice); err == nil {
			transport.Close()
			t.Error("alice logged in with her password after the switch")
		}
	})

	t.Run("a certificate is only ever its own agent", func(t *testing.T) {
		certPath, keyPath := creds.ClientCertPaths(creds.AgentDir(h.configDir, alice))
		conn, err := h.connectMapped(t, certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		own := make(chan error, 1)
		conn.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { own <- err })
		if _, err := conn.SubscribeSync("_INBOX." + bus.AgentToken(alice) + ".x"); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.SubscribeSync("_INBOX." + bus.AgentToken(bob) + ".x"); err != nil {
			t.Fatal(err)
		}
		_ = conn.Flush()
		select {
		case err := <-own:
			if !strings.Contains(err.Error(), "ermissions Violation for Subscription to \"_INBOX."+bus.AgentToken(bob)) {
				t.Errorf("unexpected server error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("alice's certificate was allowed to listen on bob's inbox")
		}
	})

	t.Run("a certificate stops working when the agent is removed", func(t *testing.T) {
		certPath, keyPath := creds.ClientCertPaths(creds.AgentDir(h.configDir, alice))
		live, err := h.connectMapped(t, certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		h.run(t, h.agentBusctl, "--direct", "agent", "remove", alice)
		for deadline := time.Now().Add(10 * time.Second); !live.IsClosed() && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		}
		if !live.IsClosed() {
			t.Error("the removed agent's certificate connection is still open")
		}
		if again, err := h.connectMapped(t, certPath, keyPath); err == nil {
			again.Close()
			t.Error("the removed agent's certificate logged in again")
		}
		if output, err := h.tryRun(h.agentBusctl, "--direct", "cert", "issue", alice); err == nil || !strings.Contains(output, "removed") {
			t.Errorf("a certificate was issued for a removed ID: %v\n%s", err, output)
		}
	})
}

func TestSwitchBackToPassword(t *testing.T) {
	h := startBus(t)
	h.switchToCert(t, bob)
	dir := creds.AgentDir(h.configDir, bob)
	certPath, keyPath := creds.ClientCertPaths(dir)
	savedCert, _ := os.ReadFile(certPath)
	savedKey, _ := os.ReadFile(keyPath)

	transport, err := broker.NewTransport(h.agentConnection(bob), bob)
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()

	h.run(t, h.agentBusctl, "--direct", "cert", "switch", bob, "--password")
	if creds.HasClientCert(dir) {
		t.Error("the certificate files are still in the agent's directory")
	}
	transport, err = broker.NewTransport(h.agentConnection(bob), bob)
	if err != nil {
		t.Fatalf("password login after the rollback: %v", err)
	}
	transport.Close()

	// The old certificate is useless now, though it has not expired.
	old := t.TempDir() + "/"
	if err := os.WriteFile(old+"c.pem", savedCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old+"k.pem", savedKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if conn, err := h.connectMapped(t, old+"c.pem", old+"k.pem"); err == nil {
		conn.Close()
		t.Error("the old certificate still logs in after the rollback")
	}
}

func TestAdminCertificateLogin(t *testing.T) {
	h := startBus(t)
	h.run(t, h.agentBusctl, "--direct", "cert", "issue", "--admin")

	// The admin tools log in with the certificates from here on.
	list := h.run(t, h.agentBusctl, "--direct", "agent", "list")
	if !strings.Contains(list, alice) || !strings.Contains(list, bob) {
		t.Errorf("agent list:\n%s", list)
	}
	h.run(t, h.agentBusctl, "--direct", "register", "late-comer@alice")
	// A password agent registered meanwhile keeps its grants after the rewrites.
	transport, err := broker.NewTransport(h.agentConnection("late-comer@alice"), "late-comer@alice")
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()

	h.run(t, h.agentBusctl, "--direct", "cert", "switch", "--admin", "--password")
	h.run(t, h.agentBusctl, "--direct", "agent", "list")
	if creds.HasClientCert(creds.AdminDir(h.configDir, broker.BusAdminUser)) {
		t.Error("the admin certificate files were not removed by the rollback")
	}
}
