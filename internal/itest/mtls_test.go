//go:build integration

package itest

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
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
		aliceHas, _ := creds.HasClientCert(creds.AgentDir(h.configDir, alice))
		bobHas, _ := creds.HasClientCert(creds.AgentDir(h.configDir, bob))
		if !aliceHas || bobHas {
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

	t.Run("alice's certificate with bob's password is never bob", func(t *testing.T) {
		certPath, keyPath := creds.ClientCertPaths(creds.AgentDir(h.configDir, alice))
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		caPEM, _ := os.ReadFile(h.caCert)
		pool.AppendCertsFromPEM(caPEM)
		bobPassword, _ := creds.ReadPassword(creds.AgentDir(h.configDir, bob))
		conn, err := nats.Connect(h.mappedAddress(), nats.UserInfo(bus.AgentToken(bob), bobPassword),
			nats.Secure(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}}),
			nats.NoReconnect(), nats.Timeout(5*time.Second))
		if err != nil {
			return // refused
		}
		defer conn.Close()
		// Connected: it must be alice, who may not listen on bob's inbox.
		violation := make(chan error, 1)
		conn.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { violation <- err })
		if _, err := conn.SubscribeSync("_INBOX." + bus.AgentToken(bob) + ".x"); err != nil {
			t.Fatal(err)
		}
		_ = conn.Flush()
		select {
		case <-violation:
		case <-time.After(5 * time.Second):
			t.Error("a client with alice's certificate and bob's password became bob")
		}
	})

	t.Run("a certificate agent cannot log in on the password port", func(t *testing.T) {
		certPath, keyPath := creds.ClientCertPaths(creds.AgentDir(h.configDir, alice))
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		caPEM, _ := os.ReadFile(h.caCert)
		pool.AppendCertsFromPEM(caPEM)
		secure := nats.Secure(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}})
		mappedName, err := creds.ClientCertIdentity(certPath)
		if err != nil {
			t.Fatal(err)
		}
		for name, option := range map[string]nats.Option{
			"by certificate alone":      nats.Name("no password"),
			"by its mapped name":        nats.UserInfo(mappedName, "anything"),
			"by its token and password": nats.UserInfo(bus.AgentToken(alice), "anything"),
		} {
			if conn, err := nats.Connect(h.address, secure, option, nats.NoReconnect(), nats.Timeout(5*time.Second)); err == nil {
				conn.Close()
				t.Errorf("logged in on the password port %s", name)
			}
		}
	})

	t.Run("a running agent reconnects with a renewed certificate by itself and can send", func(t *testing.T) {
		connection := h.agentConnection(alice)
		connection.Reconnect = &broker.Backoff{Initial: 100 * time.Millisecond, Max: 500 * time.Millisecond}
		transport, err := broker.NewTransport(connection, alice)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(transport.Close)
		aliceSession, err := adapter.Open(adapter.Config{AgentID: alice, Dir: creds.AgentDir(h.configDir, alice), Transport: transport, Directory: trust.DirectoryPath(h.configDir)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = aliceSession.Close() })
		bobSession, _ := h.startAdapter(t, bob)

		dir := creds.AgentDir(h.configDir, alice)
		oldCert, oldKey := copyPair(t, filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key"))
		h.run(t, h.agentBusctl, "--direct", "cert", "issue", alice) // the broker drops the old login
		// The old pair no longer logs in, so a send that works afterwards went over a new login
		// made with the renewed pair, read from disk at the reconnect.
		h.expectLogin(t, "the old certificate", oldCert, oldKey, false)
		deadline := time.Now().Add(20 * time.Second)
		for {
			_, err := aliceSession.Send(ctx, bob, say("after the renewal"))
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("alice never sent again after her certificate was renewed: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if got := pollUntil(t, bobSession, 2); got[1].Envelope.Body.Text != "after the renewal" { // after the first subtest's message
			t.Errorf("bob got %v", summaries(got))
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
	if has, _ := creds.HasClientCert(dir); has {
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
	if has, _ := creds.HasClientCert(creds.AdminDir(h.configDir, broker.BusAdminUser)); has {
		t.Error("the admin certificate files were not removed by the rollback")
	}
}

// copyPair saves a certificate and its key, which the next `cert issue` replaces.
func copyPair(t *testing.T, certPath, keyPath string) (savedCert, savedKey string) {
	t.Helper()
	dir := t.TempDir()
	savedCert, savedKey = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	for from, to := range map[string]string{certPath: savedCert, keyPath: savedKey} {
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return savedCert, savedKey
}

func (h *harness) expectLogin(t *testing.T, what, certPath, keyPath string, want bool) {
	t.Helper()
	conn, err := h.connectMapped(t, certPath, keyPath)
	if conn != nil {
		conn.Close()
	}
	if (err == nil) != want {
		t.Errorf("%s: logged in = %v (%v), want %v", what, err == nil, err, want)
	}
}

func TestCertificateRevocation(t *testing.T) {
	h := startBus(t)
	dir := creds.AgentDir(h.configDir, alice)
	certPath, keyPath := creds.ClientCertPaths(dir)

	t.Run("a certificate that is issued but not switched is unknown to the broker", func(t *testing.T) {
		h.run(t, h.agentBusctl, "--direct", "cert", "issue", alice)
		h.expectLogin(t, "before the switch", certPath, keyPath, false)
		h.run(t, h.agentBusctl, "--direct", "cert", "switch", alice)
		h.expectLogin(t, "after the switch", certPath, keyPath, true)
	})

	t.Run("issuing again replaces the certificate, and the old one cannot log in any more", func(t *testing.T) {
		oldCert, oldKey := copyPair(t, certPath, keyPath)
		live, err := h.connectMapped(t, oldCert, oldKey)
		if err != nil {
			t.Fatal(err)
		}
		h.run(t, h.agentBusctl, "--direct", "cert", "issue", alice)
		h.expectLogin(t, "the old certificate", oldCert, oldKey, false)
		h.expectLogin(t, "the new certificate", certPath, keyPath, true)
		for deadline := time.Now().Add(10 * time.Second); !live.IsClosed() && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		}
		if !live.IsClosed() {
			t.Error("the connection made with the old certificate is still open")
		}
	})

	t.Run("a certificate signed from a request replaces the current one", func(t *testing.T) {
		before, beforeKey := copyPair(t, certPath, keyPath)
		keyPEM, csrPEM, err := tlsinit.NewClientKeyAndCSR()
		if err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		csr, signed, key := filepath.Join(work, "csr.pem"), filepath.Join(work, "cert.pem"), filepath.Join(work, "key.pem")
		for path, data := range map[string][]byte{csr: csrPEM, key: keyPEM} {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		h.run(t, h.agentBusctl, "--direct", "cert", "sign", alice, "--csr", csr, "--out", signed)
		h.expectLogin(t, "the signed certificate", signed, key, true)
		h.expectLogin(t, "the certificate it replaced", before, beforeKey, false)
	})

	t.Run("an agent that logs in with a password has a signed certificate only once it is switched to it", func(t *testing.T) {
		keyPEM, csrPEM, err := tlsinit.NewClientKeyAndCSR()
		if err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		csr, signed, key := filepath.Join(work, "csr.pem"), filepath.Join(work, "cert.pem"), filepath.Join(work, "key.pem")
		for path, data := range map[string][]byte{csr: csrPEM, key: keyPEM} {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		h.run(t, h.agentBusctl, "--direct", "cert", "sign", bob, "--csr", csr, "--out", signed)
		h.expectLogin(t, "bob's certificate before the switch", signed, key, false)
		h.run(t, h.agentBusctl, "--direct", "cert", "switch", bob, "--cert", signed)
		h.expectLogin(t, "bob's certificate after the switch", signed, key, true)
	})

	t.Run("reissuing the admin certificates takes the old ones away", func(t *testing.T) {
		h.run(t, h.agentBusctl, "--direct", "cert", "issue", "--admin")
		adminCert, adminKey := creds.ClientCertPaths(creds.AdminDir(h.configDir, broker.ControlAdminUser))
		oldCert, oldKey := copyPair(t, adminCert, adminKey)
		h.expectLogin(t, "the first admin certificate", oldCert, oldKey, true)
		h.run(t, h.agentBusctl, "--direct", "cert", "issue", "--admin")
		h.expectLogin(t, "the old admin certificate", oldCert, oldKey, false)
		h.expectLogin(t, "the new admin certificate", adminCert, adminKey, true)
		h.run(t, h.agentBusctl, "--direct", "agent", "list") // the tools use the new ones
	})

	t.Run("switching back to the password takes the certificate away", func(t *testing.T) {
		oldCert, oldKey := copyPair(t, certPath, keyPath)
		h.run(t, h.agentBusctl, "--direct", "cert", "switch", alice, "--password")
		h.expectLogin(t, "alice's certificate after the rollback", oldCert, oldKey, false)
	})
}
