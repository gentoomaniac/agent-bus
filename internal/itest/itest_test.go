//go:build integration

// Package itest starts a real nats-server with deploy/nats.sh and checks milestone 1a end to end
// (design.md §4a, §11): the two accounts, the per-role grants, reply-subject redirection, the
// consumers busctl creates, confirmed reloads and the ledger's compare-and-set. Run it with
// `make itest`, which builds the pinned nats-server first.
package itest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	alice = "api-client@alice"
	bob   = "lib-x@bob"

	operationTimeout = 20 * time.Second
	natsScript       = "../../deploy/nats.sh"
)

// harness is a running nats-server with an initialised config directory.
type harness struct {
	configDir  string
	port       int
	busctl     string
	address    string
	monitorURL string
	caCert     string
	env        []string
	server     broker.Server
}

func startBus(t *testing.T) *harness {
	t.Helper()
	natsServer := os.Getenv("BUS_NATS_SERVER")
	if natsServer == "" {
		t.Fatal("BUS_NATS_SERVER names the pinned nats-server binary; run `make itest`, which builds it")
	}
	configDir := t.TempDir()
	port, monitorPort := freePort(t), freePort(t)
	h := &harness{
		configDir:  configDir,
		port:       port,
		busctl:     filepath.Join(t.TempDir(), "busctl"),
		address:    fmt.Sprintf("localhost:%d", port),
		monitorURL: fmt.Sprintf("http://127.0.0.1:%d", monitorPort),
		caCert:     filepath.Join(configDir, "ca.pem"),
	}
	h.env = append(os.Environ(),
		"BUS_CONFIG_DIR="+configDir,
		"BUS_NATS_SERVER="+natsServer,
		fmt.Sprintf("BUS_NATS_PORT=%d", port),
		fmt.Sprintf("BUS_MONITOR_PORT=%d", monitorPort),
		"BUS_LISTEN_IP=127.0.0.1",
		"BUS_BROKER="+h.address,
		"BUS_MONITOR_URL="+h.monitorURL,
	)
	h.server = broker.NewServer(configDir, natsServer, h.monitorURL)

	// Stop the server however the test ends, also after a start that failed half way, and make
	// sure nothing is left running.
	t.Cleanup(func() {
		pid, _ := os.ReadFile(filepath.Join(configDir, "nats", "nats-server.pid"))
		h.run(t, natsScript, "down", "--purge")
		if strings.TrimSpace(string(pid)) != "" {
			var pidNumber int
			fmt.Sscan(string(pid), &pidNumber)
			if syscall.Kill(pidNumber, 0) == nil {
				t.Errorf("nats-server (pid %d) is still running after down", pidNumber)
			}
		}
	})

	h.run(t, "go", "build", "-o", h.busctl, "../../cmd/busctl")
	h.run(t, h.busctl, "tls", "init")
	h.run(t, h.busctl, "admin", "init")
	h.run(t, natsScript, "up")
	h.run(t, h.busctl, "--direct", "init")
	h.run(t, h.busctl, "--direct", "init") // a repeat keeps what exists
	h.run(t, h.busctl, "--direct", "register", alice)
	h.run(t, h.busctl, "--direct", "register", bob)
	h.run(t, h.busctl, "--direct", "channel", "create", "lib-x", "--description", "discussion about lib-x")
	return h
}

func (h *harness) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := h.tryRun(name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, output)
	}
	return output
}

func (h *harness) tryRun(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = h.env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func timeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	t.Cleanup(cancel)
	return ctx
}

// register adds one more agent for a test that needs its own consumer state.
func (h *harness) register(t *testing.T, agentID string) {
	t.Helper()
	h.run(t, h.busctl, "--direct", "register", agentID)
}

func (h *harness) agentConnection(agentID string) broker.Connection {
	password, err := creds.ReadPassword(creds.AgentDir(h.configDir, agentID))
	if err != nil {
		panic(err)
	}
	return broker.Connection{Address: h.address, CACertPath: h.caCert, User: bus.AgentToken(agentID), Password: password}
}

func (h *harness) adminConnection(user string) broker.Connection {
	password, err := creds.ReadPassword(creds.AdminDir(h.configDir, user))
	if err != nil {
		panic(err)
	}
	return broker.Connection{Address: h.address, CACertPath: h.caCert, User: user, Password: password}
}

func (h *harness) admin(t *testing.T) bus.Admin {
	t.Helper()
	admin, err := broker.NewAdmin(h.adminConnection(broker.BusAdminUser), h.adminConnection(broker.ControlAdminUser), h.server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	return admin
}

func (h *harness) transport(t *testing.T, agentID string) bus.Transport {
	t.Helper()
	transport, err := broker.NewTransport(h.agentConnection(agentID), agentID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.Close)
	return transport
}

// client is a raw NATS connection that records what the server reports asynchronously, such as
// permission violations. The itest talks raw NATS on purpose: it proves what the server enforces,
// whatever our own client code does.
type client struct {
	conn *nats.Conn
	js   jetstream.JetStream

	mu     sync.Mutex
	errors []string
}

func (h *harness) connectRaw(t *testing.T, connection broker.Connection, inboxPrefix string) *client {
	t.Helper()
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(h.caCert)
	if err != nil {
		t.Fatal(err)
	}
	pool.AppendCertsFromPEM(caPEM)
	c := &client{}
	options := []nats.Option{
		nats.UserInfo(connection.User, connection.Password),
		nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}),
		nats.NoReconnect(),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.errors = append(c.errors, err.Error())
		}),
	}
	if inboxPrefix != "" {
		options = append(options, nats.CustomInboxPrefix(inboxPrefix))
	}
	c.conn, err = nats.Connect(h.address, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.conn.Close)
	c.js, err = jetstream.New(c.conn)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *harness) rawAgent(t *testing.T, agentID string) *client {
	return h.connectRaw(t, h.agentConnection(agentID), "_INBOX."+bus.AgentToken(agentID))
}

func (h *harness) rawAdmin(t *testing.T, user string) *client {
	return h.connectRaw(t, h.adminConnection(user), "")
}

func (c *client) serverErrors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errors...)
}

// expectViolation runs an action and waits for the server to report a permissions violation that
// names the subject. It is how a denied publish or subscription shows up: nothing else.
func (c *client) expectViolation(t *testing.T, subject string, action func()) {
	t.Helper()
	before := len(c.serverErrors())
	action()
	if err := c.conn.Flush(); err != nil {
		t.Fatalf("%s: connection closed after the action: %v", subject, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, reported := range c.serverErrors()[before:] {
			if strings.Contains(reported, "Permissions Violation") && strings.Contains(reported, subject) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("no permissions violation for %s; the server reported %v", subject, c.serverErrors()[before:])
}

func (c *client) expectNoViolation(t *testing.T, what string, action func()) {
	t.Helper()
	before := len(c.serverErrors())
	action()
	c.conn.Flush()
	time.Sleep(100 * time.Millisecond)
	if reported := c.serverErrors()[before:]; len(reported) > 0 {
		t.Errorf("%s: the server reported %v", what, reported)
	}
}

func (c *client) denyPublish(t *testing.T, subject string) {
	t.Helper()
	c.expectViolation(t, subject, func() { c.conn.Publish(subject, []byte("x")) })
}

func (c *client) denySubscribe(t *testing.T, subject string) {
	t.Helper()
	c.expectViolation(t, subject, func() { c.conn.SubscribeSync(subject) })
}

// denyRequest sends a JetStream API request that must go unanswered because the server refused it.
func (c *client) denyRequest(t *testing.T, subject string, payload string) {
	t.Helper()
	c.expectViolation(t, subject, func() {
		if reply, err := c.conn.Request(subject, []byte(payload), 500*time.Millisecond); err == nil {
			t.Errorf("%s was answered: %s", subject, reply.Data)
		}
	})
}

// pullNoWait asks a consumer for one message and says whether the server answered at all: an
// answer is either the message or a 404 status. A denied pull gets no answer.
func (c *client) pullNoWait(consumer string) (answered bool) {
	reply, err := c.conn.Request("$JS.API.CONSUMER.MSG.NEXT.BUS."+consumer, []byte(`{"batch":1,"no_wait":true}`), 500*time.Millisecond)
	return err == nil && reply != nil
}

func TestMilestone1a(t *testing.T) {
	h := startBus(t)

	t.Run("the server accepts TLS clients that trust the bus CA only", func(t *testing.T) {
		if _, err := nats.Connect(h.address, nats.UserInfo(bus.AgentToken(alice), "x"), nats.NoReconnect()); err == nil {
			t.Error("a client without the CA connected")
		}
		if err := h.connectTrusting(x509.NewCertPool()); err == nil {
			t.Error("a client with an empty trust pool connected")
		}
		bad := h.agentConnection(alice)
		bad.Password = "wrong"
		if _, err := broker.NewTransport(bad, alice); err == nil {
			t.Error("a wrong password was accepted")
		}

		// Plaintext: speak the protocol without TLS and see what the server does with it.
		raw, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.port), operationTimeout)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(5 * time.Second))
		buffer := make([]byte, 4096)
		n, _ := raw.Read(buffer)
		if !strings.Contains(string(buffer[:n]), `"tls_required":true`) {
			t.Errorf("the server's INFO does not require TLS: %s", buffer[:n])
		}
		fmt.Fprintf(raw, "CONNECT {\"user\":%q,\"pass\":\"x\",\"verbose\":false}\r\nPING\r\n", bus.AgentToken(alice))
		n, _ = raw.Read(buffer)
		if reply := string(buffer[:n]); strings.Contains(reply, "PONG") || strings.Contains(reply, "+OK") {
			t.Errorf("a plaintext CONNECT was accepted: %q", reply)
		}
	})

	t.Run("the admin users are distinct and each sees only its own account", func(t *testing.T) {
		ctx := timeout(t)
		busPassword, _ := creds.ReadPassword(creds.AdminDir(h.configDir, broker.BusAdminUser))
		controlPassword, _ := creds.ReadPassword(creds.AdminDir(h.configDir, broker.ControlAdminUser))
		if busPassword == "" || busPassword == controlPassword {
			t.Error("bus-admin and control-admin must have distinct passwords")
		}

		busAdmin, controlAdmin := h.rawAdmin(t, broker.BusAdminUser), h.rawAdmin(t, broker.ControlAdminUser)
		for _, name := range []string{"BUS"} {
			if _, err := busAdmin.js.Stream(ctx, name); err != nil {
				t.Errorf("bus-admin sees no stream %s: %v", name, err)
			}
			if _, err := controlAdmin.js.Stream(ctx, name); !errors.Is(err, jetstream.ErrStreamNotFound) {
				t.Errorf("control-admin sees stream %s in CONTROL: %v", name, err)
			}
		}
		for _, name := range []string{"LEDGER", "KV_registry", "KV_catalogue"} {
			if _, err := controlAdmin.js.Stream(ctx, name); err != nil {
				t.Errorf("control-admin sees no stream %s: %v", name, err)
			}
			if _, err := busAdmin.js.Stream(ctx, name); !errors.Is(err, jetstream.ErrStreamNotFound) {
				t.Errorf("bus-admin (publish and subscribe on >) sees stream %s of CONTROL: %v", name, err)
			}
		}
		if _, err := busAdmin.js.KeyValue(ctx, "registry"); err == nil {
			t.Error("bus-admin opened the registry bucket of CONTROL")
		}

		// Everything bus-admin may subscribe to, it still hears nothing of CONTROL's traffic.
		everything, err := busAdmin.conn.SubscribeSync(">")
		if err != nil {
			t.Fatal(err)
		}
		busAdmin.conn.Flush()
		registry, err := controlAdmin.js.KeyValue(ctx, "registry")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Put(ctx, "isolation-probe", []byte("x")); err != nil {
			t.Fatal(err)
		}
		registry.Purge(ctx, "isolation-probe")
		if _, err := controlAdmin.js.Publish(ctx, "ledger.isolation-probe", []byte("x")); err != nil {
			t.Fatal(err)
		}
		if message, err := everything.NextMsg(500 * time.Millisecond); err == nil {
			t.Errorf("bus-admin heard %s from CONTROL", message.Subject)
		}
	})

	t.Run("stream BUS, stream LEDGER and the buckets have the designed shape", func(t *testing.T) {
		ctx := timeout(t)
		busAdmin, controlAdmin := h.rawAdmin(t, broker.BusAdminUser), h.rawAdmin(t, broker.ControlAdminUser)

		stream, err := busAdmin.js.Stream(ctx, "BUS")
		if err != nil {
			t.Fatal(err)
		}
		config := stream.CachedInfo().Config
		if got, want := strings.Join(config.Subjects, " "), "dm.> general topic.> private.>"; got != want {
			t.Errorf("BUS subjects = %q, want %q", got, want)
		}
		if config.Storage != jetstream.FileStorage || config.Retention != jetstream.LimitsPolicy ||
			config.MaxAge != 0 || config.MaxBytes != -1 || config.MaxMsgs != -1 || config.Duplicates != 24*time.Hour {
			t.Errorf("BUS config = storage %v retention %v max age %v bytes %d msgs %d duplicates %v; want file, limits, infinite, 24h",
				config.Storage, config.Retention, config.MaxAge, config.MaxBytes, config.MaxMsgs, config.Duplicates)
		}

		ledger, err := controlAdmin.js.Stream(ctx, "LEDGER")
		if err != nil {
			t.Fatal(err)
		}
		if got := ledger.CachedInfo().Config.Subjects; len(got) != 1 || got[0] != "ledger.>" {
			t.Errorf("LEDGER subjects = %v", got)
		}
		for bucket, history := range map[string]int{"registry": 64, "catalogue": 1} {
			kv, err := controlAdmin.js.KeyValue(ctx, bucket)
			if err != nil {
				t.Fatal(err)
			}
			status, err := kv.Status(ctx)
			if err != nil || status.History() != int64(history) {
				t.Errorf("bucket %s: history %v (%v), want %d", bucket, status, err, history)
			}
		}
	})

	t.Run("the registry and catalogue hold what register and channel create wrote", func(t *testing.T) {
		ctx := timeout(t)
		controlAdmin := h.rawAdmin(t, broker.ControlAdminUser)
		registry, _ := controlAdmin.js.KeyValue(ctx, "registry")
		catalogue, _ := controlAdmin.js.KeyValue(ctx, "catalogue")

		entry, err := registry.Get(ctx, bus.AgentToken(bob))
		if err != nil {
			t.Fatal(err)
		}
		var registered bus.AgentRecord
		if err := json.Unmarshal(entry.Value(), &registered); err != nil || registered.AgentID != bob || registered.Fingerprint == "" {
			t.Fatalf("registry entry of %s = %s (%v)", bob, entry.Value(), err)
		}
		if len(registered.Keys) != 1 {
			t.Fatalf("registry entry of %s has %d keys, want 1", bob, len(registered.Keys))
		}
		first := registered.Keys[0]
		if first.PublicKey != registered.PublicKey || first.Fingerprint != registered.Fingerprint ||
			first.ValidFrom.IsZero() || first.RotationSig != "" || first.Revoked || first.RevokedAt != nil {
			t.Errorf("first key of %s = %+v, want the current key, valid from a time, no rotation_sig, not revoked", bob, first)
		}
		if keys, _ := registry.Keys(ctx); len(keys) != 2 {
			t.Errorf("registry keys = %v, want the two agents", keys)
		}

		entry, err = catalogue.Get(ctx, "lib-x")
		if err != nil {
			t.Fatal(err)
		}
		var channel bus.TopicRecord
		if err := json.Unmarshal(entry.Value(), &channel); err != nil || channel.Description != "discussion about lib-x" || entry.Revision() != 1 {
			t.Errorf("catalogue entry of lib-x = %s, revision %d (%v)", entry.Value(), entry.Revision(), err)
		}
		h.run(t, h.busctl, "--direct", "channel", "create", "lib-x", "--description", "updated")
		entry, _ = catalogue.Get(ctx, "lib-x")
		if json.Unmarshal(entry.Value(), &channel); channel.Description != "updated" || entry.Revision() != 2 {
			t.Errorf("after the second channel create: %s, revision %d, want the new description as revision 2", entry.Value(), entry.Revision())
		}
	})

	t.Run("register creates the two read cursors with the fixed configuration", func(t *testing.T) {
		ctx := timeout(t)
		busAdmin := h.rawAdmin(t, broker.BusAdminUser)
		for agentID, filters := range map[string]map[string]string{
			alice: {"api-client_alice__dm": "dm.api-client_alice", "api-client_alice__general": "general"},
			bob:   {"lib-x_bob__dm": "dm.lib-x_bob", "lib-x_bob__general": "general"},
		} {
			for name, filter := range filters {
				consumer, err := busAdmin.js.Consumer(ctx, "BUS", name)
				if err != nil {
					t.Errorf("%s has no consumer %s: %v", agentID, name, err)
					continue
				}
				config := consumer.CachedInfo().Config
				if config.Durable != name || config.FilterSubject != filter || config.AckPolicy != jetstream.AckExplicitPolicy ||
					config.AckWait != time.Minute || config.MaxDeliver != -1 || config.MaxAckPending != 500 ||
					config.MaxWaiting != 8 || config.InactiveThreshold != 0 || config.DeliverPolicy != jetstream.DeliverAllPolicy {
					t.Errorf("consumer %s = %+v", name, config)
				}
			}
		}
		stream, _ := busAdmin.js.Stream(ctx, "BUS")
		count := 0
		for range stream.ListConsumers(ctx).Info() {
			count++
		}
		if count != 4 {
			t.Errorf("stream BUS has %d consumers, want the 4 busctl created", count)
		}
	})

	t.Run("an agent cannot reach CONTROL, not even by naming its subjects", func(t *testing.T) {
		ctx := timeout(t)
		agent := h.rawAgent(t, alice)
		if _, err := jetstream.New(agent.conn); err != nil {
			t.Fatal(err)
		}
		agent.denyPublish(t, "$KV.registry."+bus.AgentToken(bob))
		agent.denyPublish(t, "$KV.catalogue.lib-x")
		agent.denyPublish(t, "ledger.channel.x")
		agent.denySubscribe(t, "$KV.registry.>")
		agent.denyRequest(t, "$JS.API.STREAM.INFO.KV_registry", "")
		agent.denyRequest(t, "$JS.API.STREAM.INFO.LEDGER", "")
		agent.denyRequest(t, "$JS.API.DIRECT.GET.KV_registry.$KV.registry."+bus.AgentToken(bob), "")
		if _, err := agent.js.KeyValue(ctx, "registry"); err == nil {
			t.Error("an agent opened the registry bucket")
		}
	})

	t.Run("reply subjects redirect only inside BUS: nothing reaches CONTROL, a pulled record is not stored anew, an API response is", func(t *testing.T) {
		const carol = "carol@alice"
		const consumer = "carol_alice__dm"
		h.register(t, carol)
		ctx := timeout(t)
		bobTransport := h.transport(t, bob)
		if _, err := bobTransport.Publish(ctx, bus.DMChannel(carol), "m1", []byte("redirect me")); err != nil {
			t.Fatal(err)
		}

		controlAdmin, busAdmin := h.rawAdmin(t, broker.ControlAdminUser), h.rawAdmin(t, broker.BusAdminUser)
		controlState := func() string {
			var state []string
			for _, name := range []string{"LEDGER", "KV_registry", "KV_catalogue"} {
				stream, err := controlAdmin.js.Stream(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				info, err := stream.Info(ctx)
				if err != nil {
					t.Fatal(err)
				}
				state = append(state, fmt.Sprintf("%s: %d messages, last sequence %d", name, info.State.Msgs, info.State.LastSeq))
			}
			registry, _ := controlAdmin.js.KeyValue(ctx, "registry")
			entry, _ := registry.Get(ctx, bus.AgentToken(bob))
			return strings.Join(state, "; ") + fmt.Sprintf("; bob's record revision %d: %s", entry.Revision(), entry.Value())
		}
		busState := func() (messages, lastSequence uint64) {
			stream, err := busAdmin.js.Stream(ctx, "BUS")
			if err != nil {
				t.Fatal(err)
			}
			info, err := stream.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return info.State.Msgs, info.State.LastSeq
		}

		// Every API call an agent holds, with each CONTROL subject, and busd's, as the reply subject.
		// bus-admin listens inside BUS to see where the server really delivers.
		watch, err := busAdmin.conn.SubscribeSync(">")
		if err != nil {
			t.Fatal(err)
		}
		busAdmin.conn.Flush()
		controlBefore := controlState()
		agent := h.rawAgent(t, carol)
		targets := []string{"$KV.registry." + bus.AgentToken(bob), "$KV.catalogue.lib-x", "ledger.channel.x", "busd.nonce", "busd.channel.create"}
		agent.expectNoViolation(t, "calls with reply subjects", func() {
			for _, target := range targets {
				for _, api := range []string{
					"$JS.API.CONSUMER.MSG.NEXT.BUS." + consumer,
					"$JS.API.CONSUMER.INFO.BUS." + consumer,
					"$JS.ACK.BUS." + consumer + ".1.1.1.1.0",
				} {
					agent.conn.PublishMsg(&nats.Msg{Subject: api, Reply: target, Data: []byte(`{"batch":1,"no_wait":true}`)})
				}
			}
		})
		seenTargets := map[string]bool{}
		for message, err := watch.NextMsg(time.Second); err == nil; message, err = watch.NextMsg(500 * time.Millisecond) {
			seenTargets[message.Subject] = true
		}
		t.Logf("the server delivered the redirected responses to these subjects of BUS: %v", seenTargets)
		for _, target := range targets {
			if !seenTargets[target] {
				t.Errorf("nothing was delivered to %s: the redirection did not work, so this test would prove nothing", target)
			}
		}
		if controlAfter := controlState(); controlAfter != controlBefore {
			t.Errorf("CONTROL changed after redirected calls:\nbefore %s\nafter  %s", controlBefore, controlAfter)
		}

		// A pulled record delivered to a reply subject reaches whoever listens there and is not
		// stored: the stream neither grows nor gets a record on that subject.
		const privateSubject = "private.abcdefghijklmnopqrstuvwxyz"
		agent.denyPublish(t, privateSubject)
		if _, err := bobTransport.Publish(ctx, bus.DMChannel(carol), "m2", []byte("pulled onto the private subject")); err != nil {
			t.Fatal(err)
		}
		messagesBefore, lastBefore := busState()
		listener, err := busAdmin.conn.SubscribeSync(privateSubject)
		if err != nil {
			t.Fatal(err)
		}
		busAdmin.conn.Flush()
		agent.conn.PublishMsg(&nats.Msg{Subject: "$JS.API.CONSUMER.MSG.NEXT.BUS." + consumer, Reply: privateSubject, Data: []byte(`{"batch":1,"no_wait":true}`)})
		delivered, err := listener.NextMsg(2 * time.Second)
		if err != nil || string(delivered.Data) != "pulled onto the private subject" {
			t.Fatalf("the pulled record did not reach the listener on %s: %v, %v", privateSubject, delivered, err)
		}
		time.Sleep(500 * time.Millisecond)
		stream, _ := busAdmin.js.Stream(ctx, "BUS")
		if messages, last := busState(); messages != messagesBefore || last != lastBefore {
			t.Errorf("a pulled record was stored: %d messages, last sequence %d -> %d messages, last sequence %d", messagesBefore, lastBefore, messages, last)
		}
		if stored, err := stream.GetLastMsgForSubject(ctx, privateSubject); err == nil {
			t.Errorf("a record exists on %s at sequence %d", privateSubject, stored.Sequence)
		}

		// A JetStream API response sent to a reply subject is a publish, and the stream stores it
		// anew with a new sequence. Its payload is the server's, not the sender's.
		agent.conn.PublishMsg(&nats.Msg{Subject: "$JS.API.CONSUMER.INFO.BUS." + consumer, Reply: privateSubject})
		var stored *jetstream.RawStreamMsg
		for deadline := time.Now().Add(3 * time.Second); stored == nil && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			stored, _ = stream.GetLastMsgForSubject(ctx, privateSubject)
		}
		if stored == nil {
			t.Fatalf("the API response sent to %s was not stored in BUS", privateSubject)
		}
		if !strings.Contains(string(stored.Data), "consumer_info_response") || stored.Sequence <= lastBefore {
			t.Errorf("stored response = %q at sequence %d, want the server's consumer info at a sequence after %d", stored.Data, stored.Sequence, lastBefore)
		}
		if last, err := h.admin(t).LastSequence(ctx); err != nil || last != stored.Sequence {
			t.Errorf("LastSequence = %d, %v, want the stored response's %d", last, err, stored.Sequence)
		}
	})

	t.Run("an agent sees only its own consumers, DM channel and inbox", func(t *testing.T) {
		ctx := timeout(t)
		h.register(t, "dave@alice")
		bobAgent, aliceAgent := h.rawAgent(t, bob), h.rawAgent(t, alice)
		aliceDM := "api-client_alice__dm"

		// Alice has a message to read; bob writes it, which any agent may.
		if _, err := h.transport(t, bob).Publish(ctx, bus.DMChannel(alice), "secret-1", []byte("for alice only")); err != nil {
			t.Fatal(err)
		}

		bobAgent.denyRequest(t, "$JS.API.CONSUMER.MSG.NEXT.BUS."+aliceDM, `{"batch":1,"no_wait":true}`)
		bobAgent.denyRequest(t, "$JS.API.CONSUMER.INFO.BUS."+aliceDM, "")
		bobAgent.denySubscribe(t, "dm."+bus.AgentToken(alice))
		bobAgent.denySubscribe(t, "dm.>")
		bobAgent.denySubscribe(t, "_INBOX.>")
		bobAgent.denySubscribe(t, "_INBOX."+bus.AgentToken(alice)+".>")
		bobAgent.denySubscribe(t, "busd.>")
		bobAgent.denyRequest(t, "$JS.API.DIRECT.GET.BUS", `{"seq":1}`)
		bobAgent.denyRequest(t, "$JS.API.STREAM.MSG.GET.BUS", `{"seq":1}`)
		bobAgent.denyRequest(t, "$JS.API.STREAM.INFO.BUS", "")
		bobAgent.denyRequest(t, "$JS.API.CONSUMER.LIST.BUS", "")
		bobAgent.denyRequest(t, "$JS.API.CONSUMER.NAMES.BUS", "")
		for _, subject := range []string{"private.abcdefghijklmnopqrstuvwxyz", "busd.agent.register", "busd.channel.manager", "busd.channel.create_group"} {
			bobAgent.denyPublish(t, subject)
		}

		// Bob cannot ack Alice's message: take its real ack subject from a pull by alice.
		inbox, err := aliceAgent.conn.SubscribeSync("_INBOX.api-client_alice.pull")
		if err != nil {
			t.Fatal(err)
		}
		aliceAgent.conn.PublishMsg(&nats.Msg{Subject: "$JS.API.CONSUMER.MSG.NEXT.BUS." + aliceDM, Reply: "_INBOX.api-client_alice.pull", Data: []byte(`{"batch":1,"no_wait":true}`)})
		delivered, err := inbox.NextMsg(2 * time.Second)
		if err != nil || string(delivered.Data) != "for alice only" {
			t.Fatalf("alice's pull = %v, %v", delivered, err)
		}
		bobAgent.denyPublish(t, delivered.Reply)
		busAdmin := h.rawAdmin(t, broker.BusAdminUser)
		consumer, _ := busAdmin.js.Consumer(ctx, "BUS", aliceDM)
		if info, _ := consumer.Info(ctx); info.NumAckPending != 1 {
			t.Errorf("bob's ack changed alice's consumer: %d messages pending, want 1", info.NumAckPending)
		}
		aliceAgent.expectNoViolation(t, "alice acks her own message", func() {
			if _, err := aliceAgent.conn.Request(delivered.Reply, nil, 2*time.Second); err != nil {
				t.Errorf("alice's own ack: %v", err)
			}
		})
		if info, _ := consumer.Info(ctx); info.NumAckPending != 0 {
			t.Errorf("alice's ack left %d messages pending", info.NumAckPending)
		}

		// Nobody creates, updates or deletes a consumer, their own included.
		for _, agent := range []*client{aliceAgent, bobAgent} {
			agent.denyRequest(t, "$JS.API.CONSUMER.CREATE.BUS.rogue", `{"stream_name":"BUS","config":{"name":"rogue","ack_policy":"explicit"}}`)
			agent.denyRequest(t, "$JS.API.CONSUMER.CREATE.BUS.rogue2.dm.carol_alice", `{"stream_name":"BUS","config":{"name":"rogue2","filter_subject":"dm.carol_alice","ack_policy":"explicit"}}`)
			agent.denyRequest(t, "$JS.API.CONSUMER.DURABLE.CREATE.BUS.rogue3", `{"stream_name":"BUS","config":{"durable_name":"rogue3","ack_policy":"explicit"}}`)
		}
		aliceAgent.denyRequest(t, "$JS.API.CONSUMER.CREATE.BUS.api-client_alice__dm.dm.api-client_alice", `{"stream_name":"BUS","config":{"name":"api-client_alice__dm","filter_subject":"dm.api-client_alice","ack_policy":"none"}}`)
		aliceAgent.denyRequest(t, "$JS.API.CONSUMER.DELETE.BUS.api-client_alice__dm", "")
		bobAgent.denyRequest(t, "$JS.API.CONSUMER.DELETE.BUS."+aliceDM, "")
		if info, _ := consumer.Info(ctx); info.Config.AckPolicy != jetstream.AckExplicitPolicy {
			t.Error("alice's consumer was changed")
		}

		// What an agent is granted works.
		bobAgent.expectNoViolation(t, "bob's own grants", func() {
			if !bobAgent.pullNoWait("lib-x_bob__dm") {
				t.Error("bob cannot pull his own consumer")
			}
			if _, err := bobAgent.conn.Request("$JS.API.CONSUMER.INFO.BUS.lib-x_bob__general", nil, 2*time.Second); err != nil {
				t.Errorf("bob cannot ask for his own consumer's info: %v", err)
			}
			for _, subject := range []string{"dm." + bus.AgentToken(alice), "general", "topic.lib-x", "busd.history", "busd.nonce"} {
				bobAgent.conn.Publish(subject, []byte("x"))
			}
			if _, err := bobAgent.conn.SubscribeSync("_INBOX.lib-x_bob.anything"); err != nil {
				t.Error(err)
			}
		})
	})

	t.Run("the stream sequence reaches the reader, a group cursor starts after its boundary and history moves no cursor", func(t *testing.T) {
		ctx := timeout(t)
		admin := h.admin(t)
		bobTransport, aliceTransport := h.transport(t, bob), h.transport(t, alice)
		busTransport := h.transportOnAdmin(t)

		before, err := bobTransport.Publish(ctx, bus.GroupChannel("lib-x"), "g0", []byte("before the join"))
		if err != nil {
			t.Fatal(err)
		}
		boundary, err := admin.LastSequence(ctx)
		if err != nil || boundary != before {
			t.Fatalf("LastSequence = %d, %v, want the published sequence %d", boundary, err, before)
		}
		var published []uint64
		for _, id := range []string{"g1", "g2"} {
			sequence, err := bobTransport.Publish(ctx, bus.GroupChannel("lib-x"), id, []byte("after "+id))
			if err != nil {
				t.Fatal(err)
			}
			published = append(published, sequence)
		}
		if err := admin.CreateReadCursor(ctx, alice, bus.GroupChannel("lib-x"), boundary); err != nil {
			t.Fatal(err)
		}
		if err := admin.CreateReadCursor(ctx, alice, bus.GroupChannel("lib-x"), boundary); err != nil {
			t.Fatalf("repeating CreateReadCursor: %v", err)
		}
		if err := admin.CreateReadCursor(ctx, alice, bus.DMChannel(bob), 0); err == nil {
			t.Error("a read cursor on another agent's DM channel was created")
		}

		deliveries, err := aliceTransport.Fetch(ctx, bus.GroupChannel("lib-x"), 10)
		if err != nil || len(deliveries) != 2 {
			t.Fatalf("alice fetched %d records (%v), want the two after the boundary", len(deliveries), err)
		}
		for i, delivery := range deliveries {
			if delivery.Sequence != published[i] {
				t.Errorf("delivery %d has sequence %d, want the publish's %d", i, delivery.Sequence, published[i])
			}
		}

		busAdmin := h.rawAdmin(t, broker.BusAdminUser)
		consumer, _ := busAdmin.js.Consumer(ctx, "BUS", "api-client_alice__g__lib-x")
		infoBefore, _ := consumer.Info(ctx)
		history, err := busTransport.History(ctx, bus.GroupChannel("lib-x"), 0, 10)
		if err != nil || len(history) < 3 {
			t.Fatalf("history = %v (%v), want every record of the channel", history, err)
		}
		wantTail := []uint64{before, published[0], published[1]}
		for i, record := range history[len(history)-3:] {
			if record.Sequence != wantTail[i] {
				t.Errorf("history ends with sequences %v, want %v in canonical order", history, wantTail)
				break
			}
		}
		infoAfter, _ := consumer.Info(ctx)
		position := func(info *jetstream.ConsumerInfo) [5]uint64 {
			return [5]uint64{info.Delivered.Stream, info.Delivered.Consumer, info.AckFloor.Stream, uint64(info.NumAckPending), uint64(info.NumPending)}
		}
		if position(infoBefore) != position(infoAfter) {
			t.Errorf("reading history moved the read cursor: %v -> %v (delivered stream, delivered consumer, ack floor, ack pending, pending)", position(infoBefore), position(infoAfter))
		}

		for _, delivery := range deliveries {
			if err := delivery.Ack(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if rest, err := aliceTransport.Fetch(ctx, bus.GroupChannel("lib-x"), 10); err != nil || len(rest) != 0 {
			t.Errorf("after the acks alice fetched %d more records (%v)", len(rest), err)
		}

		if err := admin.DeleteReadCursor(ctx, alice, bus.GroupChannel("lib-x")); err != nil {
			t.Fatal(err)
		}
		if err := admin.DeleteReadCursor(ctx, alice, bus.GroupChannel("lib-x")); err != nil {
			t.Errorf("deleting a cursor twice: %v", err)
		}
		if _, err := busAdmin.js.Consumer(ctx, "BUS", "api-client_alice__g__lib-x"); !errors.Is(err, jetstream.ErrConsumerNotFound) {
			t.Errorf("the deleted cursor is still there: %v", err)
		}
	})

	t.Run("a denied call reports the server's reason", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// bob has no consumer for a group channel nobody created.
		_, err := h.transport(t, bob).Fetch(ctx, bus.GroupChannel("nobody-made-this"), 1)
		if err == nil || !strings.Contains(err.Error(), "Permissions Violation") {
			t.Errorf("Fetch on an ungranted consumer = %v, want a timeout that names the permissions violation", err)
		}
	})

	t.Run("MaxAckPending 500 holds back the rest and MaxDeliver -1 never drops a message", func(t *testing.T) {
		ctx := timeout(t)
		const dave = "dave@alice"
		bobTransport, daveTransport := h.transport(t, bob), h.transport(t, dave)
		for i := range 600 {
			if _, err := bobTransport.Publish(ctx, bus.DMChannel(dave), fmt.Sprint("bulk-", i), []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		first, err := daveTransport.Fetch(ctx, bus.DMChannel(dave), 600)
		if err != nil || len(first) != 500 {
			t.Fatalf("fetched %d of 600 (%v), want 500 while none is acked", len(first), err)
		}
		if blocked, _ := daveTransport.Fetch(ctx, bus.DMChannel(dave), 100); len(blocked) != 0 {
			t.Errorf("another fetch returned %d records while 500 are unacknowledged", len(blocked))
		}
		for _, delivery := range first {
			if err := delivery.Ack(ctx); err != nil {
				t.Fatal(err)
			}
		}
		rest, err := daveTransport.Fetch(ctx, bus.DMChannel(dave), 600)
		if err != nil || len(rest) != 100 {
			t.Fatalf("after the acks fetched %d (%v), want the remaining 100", len(rest), err)
		}

		// The same configuration with a short AckWait: an unacknowledged message comes back every
		// time, with no delivery limit.
		busAdmin := h.rawAdmin(t, broker.BusAdminUser)
		real, _ := busAdmin.js.Consumer(ctx, "BUS", "carol_alice__dm")
		config := real.CachedInfo().Config
		config.Durable, config.Name, config.AckWait, config.FilterSubject = "probe__dm", "probe__dm", time.Second, "dm.probe_nobody"
		probe, err := busAdmin.js.CreateConsumer(ctx, "BUS", config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := busAdmin.js.Publish(ctx, "dm.probe_nobody", []byte("again")); err != nil {
			t.Fatal(err)
		}
		deliveries := 0
		for range 5 {
			batch, err := probe.Fetch(1, jetstream.FetchMaxWait(1500*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			for range batch.Messages() {
				deliveries++
			}
		}
		if deliveries != 5 {
			t.Errorf("an unacknowledged message was delivered %d times in 5 tries, want 5", deliveries)
		}
	})

	t.Run("Nats-Msg-Id dedupe is per destination subject and envelope ID", func(t *testing.T) {
		ctx := timeout(t)
		admin := h.admin(t)
		transport := h.transport(t, alice)

		first, err := transport.Publish(ctx, bus.GroupChannel("lib-x"), "dup-1", []byte("once"))
		if err != nil {
			t.Fatal(err)
		}
		last, _ := admin.LastSequence(ctx)
		again, err := transport.Publish(ctx, bus.GroupChannel("lib-x"), "dup-1", []byte("once"))
		if err != nil || again != first {
			t.Errorf("a retry returned sequence %d (%v), want the first one, %d", again, err, first)
		}
		if now, _ := admin.LastSequence(ctx); now != last {
			t.Errorf("the retry was stored: last sequence %d -> %d", last, now)
		}
		other, err := transport.Publish(ctx, bus.GeneralChannel(), "dup-1", []byte("once"))
		if err != nil || other <= first {
			t.Errorf("the same ID on general returned %d (%v), want a new record after %d", other, err, first)
		}

		busAdmin := h.rawAdmin(t, broker.BusAdminUser)
		stream, _ := busAdmin.js.Stream(ctx, "BUS")
		stored, err := stream.GetMsg(ctx, first)
		if err != nil || stored.Header.Get("Nats-Msg-Id") != "topic.lib-x/dup-1" {
			t.Errorf("stored message header Nats-Msg-Id = %q (%v), want the destination subject and the ID", stored.Header.Get("Nats-Msg-Id"), err)
		}
	})

	t.Run("LEDGER refuses an operation that expects a stale last sequence", func(t *testing.T) {
		ctx := timeout(t)
		ledger := h.rawAdmin(t, broker.ControlAdminUser).js
		const subject = "ledger.channel.c1"

		first, err := ledger.Publish(ctx, subject, []byte("create"), jetstream.WithExpectLastSequencePerSubject(0))
		if err != nil {
			t.Fatalf("the first operation on a subject: %v", err)
		}
		wrongSequence := func(err error) bool {
			var apiErr *jetstream.APIError
			return errors.As(err, &apiErr) && apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence
		}
		if _, err := ledger.Publish(ctx, subject, []byte("again"), jetstream.WithExpectLastSequencePerSubject(0)); !wrongSequence(err) {
			t.Errorf("a second creation expecting sequence 0: %v, want a wrong-last-sequence error", err)
		}
		second, err := ledger.Publish(ctx, subject, []byte("invite"), jetstream.WithExpectLastSequencePerSubject(first.Sequence))
		if err != nil {
			t.Fatalf("an operation expecting the current sequence: %v", err)
		}
		if _, err := ledger.Publish(ctx, subject, []byte("stale"), jetstream.WithExpectLastSequencePerSubject(first.Sequence)); !wrongSequence(err) {
			t.Errorf("a stale operation: %v, want a wrong-last-sequence error", err)
		}
		if _, err := ledger.Publish(ctx, "ledger.channel.c2", []byte("other"), jetstream.WithExpectLastSequencePerSubject(0)); err != nil {
			t.Errorf("another channel's first operation: %v", err)
		}
		stream, _ := ledger.Stream(ctx, "LEDGER")
		last, err := stream.GetLastMsgForSubject(ctx, subject)
		if err != nil || last.Sequence != second.Sequence {
			t.Errorf("last message of %s = %v (%v), want sequence %d", subject, last, err, second.Sequence)
		}
	})

	t.Run("a reload takes effect on live connections and is confirmed through the monitoring endpoint", func(t *testing.T) {
		const erin, erinToken = "erin@alice", "erin_alice"
		const groupConsumer = erinToken + "__g__newchan"
		ctx := timeout(t)
		h.register(t, erin)
		admin := h.admin(t)
		agent := h.rawAgent(t, erin)
		closed := make(chan struct{})
		agent.conn.SetClosedHandler(func(*nats.Conn) { close(closed) })

		// The consumer exists before the channel does, so only the grant is missing.
		if err := admin.CreateReadCursor(ctx, erin, bus.GroupChannel("newchan"), 0); err != nil {
			t.Fatal(err)
		}
		agent.denyRequest(t, "$JS.API.CONSUMER.MSG.NEXT.BUS."+groupConsumer, `{"batch":1,"no_wait":true}`)

		loadedBefore, generationBefore := h.configLoadTime(t), h.authGeneration(t)
		h.run(t, h.busctl, "--direct", "channel", "create", "newchan", "--description", "reload test")
		if loaded := h.configLoadTime(t); !loaded.After(loadedBefore) {
			t.Errorf("config load time %v did not move past %v after the reload", loaded, loadedBefore)
		}
		if generation := h.authGeneration(t); generation != generationBefore+1 {
			t.Errorf("auth.conf generation %d, want %d", generation, generationBefore+1)
		}
		if !agent.pullNoWait(groupConsumer) {
			t.Error("the new grant does not work on the live connection")
		}
		if agent.conn.Status() != nats.CONNECTED {
			t.Error("the reload dropped the live connection")
		}

		// An invalid candidate is refused before it replaces the file, and the server keeps running.
		authBefore, _ := os.ReadFile(filepath.Join(h.server.Dir, "auth.conf"))
		loadedBefore = h.configLoadTime(t)
		err := h.server.UpdateAuth(ctx, func(string) (string, error) { return "users = [ {{{", nil })
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("an invalid candidate: %v, want a refusal", err)
		}
		if authAfter, _ := os.ReadFile(filepath.Join(h.server.Dir, "auth.conf")); string(authAfter) != string(authBefore) {
			t.Error("an invalid candidate replaced auth.conf")
		}
		if !h.configLoadTime(t).Equal(loadedBefore) {
			t.Error("the server reloaded although the candidate was refused")
		}

		// Revoking a grant takes effect on the live connection too.
		err = h.server.UpdateAuth(ctx, func(body string) (string, error) {
			revoked := strings.Replace(body, `"$JS.API.CONSUMER.MSG.NEXT.BUS.`+groupConsumer+`", `, "", 1)
			if revoked == body {
				return "", errors.New("the grant is not in auth.conf")
			}
			return revoked, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if agent.pullNoWait(groupConsumer) {
			t.Error("the revoked grant still works on the live connection")
		}
		if !agent.pullNoWait("erin_alice__dm") {
			t.Error("the revoke took away a grant it should not")
		}

		// Removing the account disconnects it and keeps it out.
		if err := admin.DeleteAccount(ctx, erin); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("the removed agent is still connected")
		}
		if _, err := broker.NewTransport(h.agentConnection(erin), erin); err == nil {
			t.Error("a removed agent connected again")
		}
		if exists, err := admin.AccountExists(ctx, erin); err != nil || exists {
			t.Errorf("AccountExists after DeleteAccount = %v, %v", exists, err)
		}
	})

	t.Run("busctl needs --direct, refuses a second registration and gives up on a dead broker", func(t *testing.T) {
		if output, err := h.tryRun(h.busctl, "init"); err == nil {
			t.Errorf("init without --direct succeeded:\n%s", output)
		}
		if output, err := h.tryRun(h.busctl, "--direct", "register", alice); err == nil {
			t.Errorf("second register succeeded:\n%s", output)
		}
		const limit = 2 * time.Second
		start := time.Now()
		output, err := h.tryRun(h.busctl, "--direct", "--broker", fmt.Sprintf("localhost:%d", freePort(t)), "--timeout", limit.String(), "init")
		if err == nil {
			t.Fatalf("init against a dead broker succeeded:\n%s", output)
		}
		if elapsed := time.Since(start); elapsed > limit+operationTimeout {
			t.Errorf("busctl took %v to give up, want about %v", elapsed, limit)
		}
	})
}

// connectTrusting tries to connect trusting only the given pool and returns the error, or nil if
// it connected.
func (h *harness) connectTrusting(pool *x509.CertPool) error {
	conn, err := nats.Connect(h.address, nats.UserInfo(bus.AgentToken(alice), "x"), nats.NoReconnect(),
		nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
	if err == nil {
		conn.Close()
	}
	return err
}

// transportOnAdmin is a transport on the bus-admin connection, which may read any history.
func (h *harness) transportOnAdmin(t *testing.T) bus.Transport {
	t.Helper()
	transport, err := broker.NewTransport(h.adminConnection(broker.BusAdminUser), "history@reader")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.Close)
	return transport
}

// configLoadTime is what the server's monitoring endpoint says about its last configuration load.
func (h *harness) configLoadTime(t *testing.T) time.Time {
	t.Helper()
	response, err := http.Get(h.monitorURL + "/varz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var varz struct {
		ConfigLoadTime time.Time `json:"config_load_time"`
	}
	if err := json.NewDecoder(response.Body).Decode(&varz); err != nil {
		t.Fatal(err)
	}
	return varz.ConfigLoadTime
}

// authGeneration is the generation number in auth.conf's first line.
func (h *harness) authGeneration(t *testing.T) int {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(h.server.Dir, "auth.conf"))
	if err != nil {
		t.Fatal(err)
	}
	var generation int
	if _, err := fmt.Sscanf(string(content), "# generation %d\n", &generation); err != nil {
		t.Fatalf("auth.conf has no generation header: %v", err)
	}
	return generation
}
