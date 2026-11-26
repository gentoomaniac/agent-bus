//go:build integration

package itest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"github.com/nats-io/nats.go"
)

const carol = "newcomer@carol"

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// startBusd runs busd against the harness's broker, as the broker container does, and waits until
// it answers.
func (h *harness) startBusd(t *testing.T) {
	t.Helper()
	output := h.launchBusd(t)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := h.tryRun(h.agentBusctl, "ca", "list"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("busd never answered; it said:\n%s", output)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// launchBusd starts busd without waiting for it to answer, as the container does before
// nats-server is up.
func (h *harness) launchBusd(t *testing.T) *lockedBuffer {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "busd")
	h.run(t, "go", "build", "-o", binary, "../../cmd/busd")
	output := &lockedBuffer{}
	cmd := exec.Command(binary, "-vv")
	cmd.Env = h.env
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("busd said:\n%s", output)
		}
	})
	return output
}

// enrollee is an `agent-bus enroll` run on another machine: a fresh config directory.
type enrollee struct {
	configDir string
	output    *lockedBuffer
	done      chan error
}

func (h *harness) buildAgentBus(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "agent-bus")
	h.run(t, "go", "build", "-o", binary, "../../cmd/agent-bus")
	return binary
}

func (h *harness) startEnrollee(t *testing.T, agentBus, agentID string, args ...string) *enrollee {
	t.Helper()
	password, err := creds.ReadPassword(filepath.Join(h.configDir, "enroll"))
	if err != nil {
		t.Fatal(err)
	}
	e := &enrollee{configDir: t.TempDir(), output: &lockedBuffer{}, done: make(chan error, 1)}
	args = append([]string{"enroll", "--agent", agentID, "--enroll-password", password}, args...)
	cmd := exec.Command(agentBus, args...)
	cmd.Env = append(h.env, "BUS_CONFIG_DIR="+e.configDir)
	cmd.Stdout, cmd.Stderr = e.output, e.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { e.done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return e
}

// wait returns how the run ended.
func (e *enrollee) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-e.done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("agent-bus enroll did not finish; it said:\n%s", e.output)
		return nil
	}
}

// pendingCode waits until `ca list` shows a request for the agent and returns its code.
func (h *harness) pendingCode(t *testing.T, agentID string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		list := h.run(t, h.agentBusctl, "ca", "list")
		for _, line := range strings.Split(list, "\n") {
			if fields := strings.Fields(line); len(fields) > 2 && fields[1] == agentID {
				return fields[0]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending request for %s:\n%s", agentID, list)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (h *harness) caFingerprint(t *testing.T) string {
	return strings.TrimSpace(h.run(t, h.agentBusctl, "ca", "fingerprint"))
}

// agentAt runs the adapter of an enrolled agent on its own machine's config directory, as
// `agent-bus mcp` does: with its directory loop, which syncs with busd and follows the heads.
func (h *harness) agentAt(t *testing.T, configDir, agentID string) *adapter.Adapter {
	t.Helper()
	caPath := filepath.Join(configDir, "ca.pem")
	connection, err := broker.UserConnection(creds.AgentDir(configDir, agentID), bus.AgentToken(agentID), h.address, h.mappedPort, caPath)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := broker.NewTransport(connection, agentID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(adapter.Config{
		AgentID: agentID, Dir: creds.AgentDir(configDir, agentID), Transport: transport, Directory: trust.DirectoryPath(configDir),
		DirectoryLink: transport, CACert: caPEM,
	})
	if err != nil {
		transport.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close(); transport.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.RunDirectory(ctx, time.Hour, func(err error) { t.Logf("%s syncing the directory: %v", agentID, err) })
	}()
	t.Cleanup(func() { cancel(); <-done }) // before the session closes: cleanups run last first
	return session
}

func certURIs(t *testing.T, certPath string) []string {
	t.Helper()
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, uri := range cert.URIs {
		uris = append(uris, uri.String())
	}
	return uris
}

func TestEnrollment(t *testing.T) {
	h := startBus(t)
	h.run(t, h.agentBusctl, "enroll", "init")
	h.startBusd(t)
	agentBus := h.buildAgentBus(t)
	password, _ := creds.ReadPassword(filepath.Join(h.configDir, "enroll"))

	testRun := t                           // outlives the subtests, whose temporary files are removed when each ends
	var firstCertPath, firstKeyPath string // the certificate that enrolled carol first, for the replacement below

	t.Run("a new machine enrolls with the owner's CA fingerprint, is approved and talks to an existing agent", func(t *testing.T) {
		e := h.startEnrollee(t, agentBus, carol, "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		code := h.pendingCode(t, carol)
		if !strings.Contains(e.output.String(), code) {
			t.Errorf("the machine printed no approval code %s:\n%s", code, e.output)
		}
		if has, _ := creds.HasClientCert(creds.AgentDir(e.configDir, carol)); has {
			t.Error("a certificate exists before approval")
		}
		h.run(t, h.agentBusctl, "ca", "sign", code)
		if err := e.wait(t); err != nil {
			t.Fatalf("enroll failed: %v\n%s", err, e.output)
		}

		agentDir := creds.AgentDir(e.configDir, carol)
		certPath, keyPath := creds.ClientCertPaths(agentDir)
		if uris := certURIs(t, certPath); len(uris) != 1 || !creds.MatchesIdentity(uris[0], "agentbus:agent/newcomer_carol") || uris[0] == "agentbus:agent/newcomer_carol" {
			t.Errorf("the certificate names %v", uris)
		}
		if err := creds.ValidateClientCert(certPath, keyPath, broker.AgentCertIdentity(carol)); err != nil {
			t.Error(err)
		}
		caPEM, _ := os.ReadFile(filepath.Join(e.configDir, "ca.pem"))
		wantCA, _ := os.ReadFile(h.caCert)
		if !bytes.Equal(caPEM, wantCA) {
			t.Error("the CA fetched by fingerprint differs from the owner's")
		}
		if _, err := identity.ReadPrivateKey(agentDir); err != nil {
			t.Errorf("no signing key: %v", err)
		}
		if _, err := os.Stat(filepath.Join(agentDir, "enroll-pending.json")); err == nil {
			t.Error("the pending request file is left behind")
		}

		firstCertPath, firstKeyPath = copyPair(testRun, certPath, keyPath)
		carolSession := h.agentAt(t, e.configDir, carol)
		aliceSession, _ := h.startAdapter(t, alice)
		if _, err := aliceSession.Send(timeout(t), carol, say("welcome, newcomer")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, carolSession, 1); got[0].Envelope.Body.Text != "welcome, newcomer" {
			t.Errorf("the newcomer got %v", summaries(got))
		}
		if _, err := carolSession.Send(timeout(t), alice, say("glad to be here")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, aliceSession, 1); got[0].Envelope.Body.Text != "glad to be here" {
			t.Errorf("alice got %v", summaries(got))
		}
		// The agent has no password login: it exists only as a certificate account.
		if has, _ := creds.HasClientCert(creds.AgentDir(h.configDir, carol)); has {
			t.Error("busd left a certificate in its own config directory")
		}
		list := h.run(t, h.agentBusctl, "--direct", "agent", "list")
		if !strings.Contains(list, carol) {
			t.Errorf("the registry lacks the enrolled agent:\n%s", list)
		}
	})

	t.Run("a rejected request fails cleanly with the reason and creates nothing", func(t *testing.T) {
		const rejected = "rejected@carol"
		e := h.startEnrollee(t, agentBus, rejected, "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		code := h.pendingCode(t, rejected)
		h.run(t, h.agentBusctl, "ca", "reject", code, "--reason", "unknown laptop")
		err := e.wait(t)
		if err == nil || !strings.Contains(e.output.String(), "unknown laptop") {
			t.Fatalf("a rejected enrollment: %v\n%s", err, e.output)
		}
		if has, _ := creds.HasClientCert(creds.AgentDir(e.configDir, rejected)); has {
			t.Error("a certificate was installed after a rejection")
		}
		if exists, err := h.admin(t).AccountExists(timeout(t), rejected); err != nil || exists {
			t.Errorf("an account exists for the rejected ID: %v, %v", exists, err)
		}
		if list := h.run(t, h.agentBusctl, "ca", "list"); strings.Contains(list, rejected) {
			t.Errorf("the rejected request is still pending:\n%s", list)
		}
	})

	t.Run("a machine without the CA or a fingerprint trusts nothing the broker says", func(t *testing.T) {
		e := h.startEnrollee(t, agentBus, "nofp@carol")
		if err := e.wait(t); err == nil || !strings.Contains(e.output.String(), "--ca-fingerprint") {
			t.Errorf("enroll without a CA: %v\n%s", err, e.output)
		}
		wrong := h.startEnrollee(t, agentBus, "wrongfp@carol", "--ca-fingerprint", strings.Repeat("ab", 32))
		if err := wrong.wait(t); err == nil || !strings.Contains(wrong.output.String(), "fingerprint") {
			t.Errorf("enroll with the wrong fingerprint: %v\n%s", err, wrong.output)
		}
		if _, err := os.Stat(filepath.Join(wrong.configDir, "ca.pem")); err == nil {
			t.Error("a CA that does not match the fingerprint was saved")
		}
		if list := h.run(t, h.agentBusctl, "ca", "list"); strings.Contains(list, "wrongfp") || strings.Contains(list, "nofp") {
			t.Errorf("a request was sent without a trusted CA:\n%s", list)
		}
	})

	t.Run("autosign approves a request with the secret and logs it, and nothing without it", func(t *testing.T) {
		out := h.run(t, h.agentBusctl, "enroll", "autosign")
		secret := strings.TrimSpace(strings.Split(strings.Split(out, "\n\n")[1], "\n")[0])
		auto := h.startEnrollee(t, agentBus, "auto@carol", "--waitforcert", "--ca-fingerprint", h.caFingerprint(t), "--secret", secret)
		if err := auto.wait(t); err != nil {
			t.Fatalf("autosigned enroll failed: %v\n%s", err, auto.output)
		}
		if has, _ := creds.HasClientCert(creds.AgentDir(auto.configDir, "auto@carol")); !has {
			t.Error("no certificate after autosign")
		}
		wrong := h.startEnrollee(t, agentBus, "guess@carol", "--ca-fingerprint", h.caFingerprint(t), "--secret", "not the secret")
		if err := wrong.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, wrong.output)
		}
		h.pendingCode(t, "guess@carol") // still waiting for the owner
		h.run(t, h.agentBusctl, "enroll", "autosign", "--off")
		off := h.startEnrollee(t, agentBus, "later@carol", "--ca-fingerprint", h.caFingerprint(t), "--secret", secret)
		if err := off.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, off.output)
		}
		h.pendingCode(t, "later@carol") // the secret no longer counts
	})

	t.Run("a request for an ID that has another key waits for an explicit replace", func(t *testing.T) {
		e := h.startEnrollee(t, agentBus, bob, "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		code := h.pendingCode(t, bob)
		if list := h.run(t, h.agentBusctl, "ca", "list"); !strings.Contains(list, enroll.IdentityReplaces) {
			t.Errorf("the list does not flag the replacement:\n%s", list)
		}
		if output, err := h.tryRun(h.agentBusctl, "ca", "sign", code); err == nil || !strings.Contains(output, "--replace") {
			t.Fatalf("signing a replacement without confirmation: %v\n%s", err, output)
		}
		h.run(t, h.agentBusctl, "ca", "sign", code, "--replace")
		if err := e.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, e.output)
		}
		records, err := h.admin(t).Agents(timeout(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.AgentID == bob && (len(record.Keys) != 2 || !record.Keys[0].Revoked) {
				t.Errorf("bob's key history after replacement: %+v", record.Keys)
			}
		}
	})

	t.Run("enrolling an ID again with --replace takes the old machine's certificate away", func(t *testing.T) {
		oldCert, oldKey := firstCertPath, firstKeyPath
		if conn, err := h.connectMapped(t, oldCert, oldKey); err != nil {
			t.Fatalf("the first certificate does not log in before the replacement: %v", err)
		} else {
			conn.Close()
		}
		second := h.startEnrollee(t, agentBus, carol, "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		code := h.pendingCode(t, carol)
		h.run(t, h.agentBusctl, "ca", "sign", code, "--replace")
		if err := second.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, second.output)
		}
		newCert, newKey := creds.ClientCertPaths(creds.AgentDir(second.configDir, carol))
		if conn, err := h.connectMapped(t, newCert, newKey); err != nil {
			t.Errorf("the replacement's certificate does not log in: %v", err)
		} else {
			conn.Close()
		}
		if conn, err := h.connectMapped(t, oldCert, oldKey); err == nil {
			conn.Close()
			t.Error("the replaced machine's certificate still logs in")
		}
	})

	t.Run("an approval and agent-busctl --direct never run at the same time", func(t *testing.T) {
		e := h.startEnrollee(t, agentBus, "locked@carol", "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		code := h.pendingCode(t, "locked@carol")

		unlock, err := h.server.LockMutations(time.Second) // what a running agent-busctl --direct holds
		if err != nil {
			t.Fatal(err)
		}
		type result struct {
			output string
			err    error
		}
		signed := make(chan result, 1)
		go func() {
			output, err := h.tryRun(h.agentBusctl, "ca", "sign", code)
			signed <- result{output, err}
		}()
		if output, err := h.tryRun(h.agentBusctl, "--timeout", "1s", "--direct", "agent", "list"); err == nil || !strings.Contains(output, "another process holds") {
			t.Errorf("agent-busctl --direct ran while the lock was held: %v\n%s", err, output)
		}
		select {
		case r := <-signed:
			t.Fatalf("the approval ran while the lock was held: %v\n%s", r.err, r.output)
		case <-time.After(1500 * time.Millisecond):
		}

		unlock()
		select {
		case r := <-signed:
			if r.err != nil {
				t.Fatalf("the approval after the release: %v\n%s", r.err, r.output)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the approval never ran after the release")
		}
		if err := e.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, e.output)
		}
	})

	t.Run("an enrollee never receives a certificate for an ID the owner did not approve for it", func(t *testing.T) {
		spy := h.connectRaw(t, broker.Connection{User: enroll.EnrollUser, Password: password}, enroll.InboxPrefix+".spy")
		overheard := make(chan *nats.Msg, 64)
		if _, err := spy.conn.ChanSubscribe(enroll.InboxPrefix+".>", overheard); err != nil {
			t.Fatal(err)
		}
		spy.conn.Flush()

		mallory := h.startEnrollee(t, agentBus, "mallory@carol", "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		victim := h.startEnrollee(t, agentBus, "victim@carol", "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
		malloryCode := h.pendingCode(t, "mallory@carol")
		victimCode := h.pendingCode(t, "victim@carol")
		h.run(t, h.agentBusctl, "ca", "sign", victimCode)
		if err := victim.wait(t); err != nil {
			t.Fatalf("%v\n%s", err, victim.output)
		}
		if has, _ := creds.HasClientCert(creds.AgentDir(mallory.configDir, "mallory@carol")); has {
			t.Error("mallory got a certificate that the owner never approved")
		}
		// Everything the shared login could overhear is public and names only the approved agent.
		deadline := time.After(time.Second)
	listening:
		for {
			select {
			case message := <-overheard:
				if strings.Contains(string(message.Data), "PRIVATE KEY") {
					t.Errorf("a reply carries a private key: %s", message.Data)
				}
				if strings.Contains(string(message.Data), "mallory") && strings.Contains(string(message.Data), `"status":"approved"`) {
					t.Errorf("a reply approves mallory: %s", message.Data)
				}
			case <-deadline:
				break listening
			}
		}
		h.run(t, h.agentBusctl, "ca", "reject", malloryCode)
		if err := mallory.wait(t); err == nil {
			t.Error("mallory's enrollment succeeded")
		}
		victimCert, _ := creds.ClientCertPaths(creds.AgentDir(victim.configDir, "victim@carol"))
		if uris := certURIs(t, victimCert); len(uris) != 1 || !creds.MatchesIdentity(uris[0], "agentbus:agent/victim_carol") {
			t.Errorf("the victim's certificate names %v", uris)
		}
	})

	t.Run("the enrollment login can only ask, and sees nothing of the bus", func(t *testing.T) {
		c := h.connectRaw(t, broker.Connection{User: enroll.EnrollUser, Password: password}, enroll.InboxPrefix+".t")
		for _, subject := range []string{
			"dm." + bus.AgentToken(alice), "general", "topic.lib-x", "presence." + bus.AgentToken(alice),
			enroll.SubjectList, enroll.SubjectSign, enroll.SubjectReject, enroll.SubjectPending,
			"busd.agent.register", "$KV.registry.x", "ledger.x",
		} {
			c.denyPublish(t, subject)
		}
		for _, subject := range []string{">", "dm.>", "general", "_INBOX.>", "_INBOX." + bus.AgentToken(alice) + ".>", "busd.>", enroll.SubjectPending, "$JS.API.>", "$KV.>"} {
			c.denySubscribe(t, subject)
		}
		c.denyRequest(t, "$JS.API.STREAM.INFO.BUS", "")
		c.denyRequest(t, "$JS.API.CONSUMER.MSG.NEXT.BUS."+bus.AgentToken(alice)+"__dm", `{"batch":1}`)
		c.expectNoViolation(t, "an enrollment status question", func() {
			if _, err := c.conn.Request(enroll.SubjectStatus, []byte(`{"request_id":"none"}`), 2*time.Second); err != nil {
				t.Errorf("busd did not answer a status question: %v", err)
			}
		})
	})

	t.Run("busd answers only into inbox space, so a chosen reply subject cannot make it write to CONTROL", func(t *testing.T) {
		control := h.rawAdmin(t, broker.ControlAdminUser)
		// streamState is what the attacks below would change: a KV write or a purge moves it, a
		// deleted stream is an error.
		streamState := func(stream string) string {
			reply, err := control.conn.Request("$JS.API.STREAM.INFO."+stream, nil, 2*time.Second)
			if err != nil {
				t.Fatalf("stream %s: %v", stream, err)
			}
			var info struct {
				State struct {
					Messages uint64 `json:"messages"`
					LastSeq  uint64 `json:"last_seq"`
				} `json:"state"`
				Error *struct{ Description string } `json:"error"`
			}
			if err := json.Unmarshal(reply.Data, &info); err != nil || info.Error != nil {
				t.Fatalf("stream %s: %s", stream, reply.Data)
			}
			return fmt.Sprintf("%d messages, last sequence %d", info.State.Messages, info.State.LastSeq)
		}
		streams := []string{"KV_registry", "KV_catalogue", "LEDGER"}
		before := map[string]string{}
		for _, stream := range streams {
			before[stream] = streamState(stream)
		}
		recordsBefore, err := h.admin(t).Agents(timeout(t))
		if err != nil {
			t.Fatal(err)
		}

		enrollee := h.connectRaw(t, broker.Connection{User: enroll.EnrollUser, Password: password}, enroll.InboxPrefix+".attack")
		for _, reply := range []string{
			"$KV.registry." + bus.AgentToken(alice), "$KV.registry.>", "$JS.API.STREAM.DELETE.LEDGER", "$JS.API.STREAM.PURGE.KV_registry",
			"$JS.API.STREAM.DELETE.KV_registry", enroll.SubjectPending, "_INBOX.agent-busctl.x", "_INBOX.>", enroll.InboxPrefix + ".>",
		} {
			for _, subject := range []string{enroll.SubjectStatus, enroll.SubjectRequest} {
				if err := enrollee.conn.PublishMsg(&nats.Msg{Subject: subject, Reply: reply, Data: []byte(`{"request_id":"none"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			// The owner's endpoints are called by the admin login, which may publish anywhere.
			for _, subject := range []string{enroll.SubjectList, enroll.SubjectSign, enroll.SubjectReject} {
				if err := control.conn.PublishMsg(&nats.Msg{Subject: subject, Reply: reply, Data: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		enrollee.conn.Flush()
		control.conn.Flush()
		time.Sleep(500 * time.Millisecond)

		for _, stream := range streams {
			if after := streamState(stream); after != before[stream] {
				t.Errorf("stream %s changed from %q to %q", stream, before[stream], after)
			}
		}
		recordsAfter, err := h.admin(t).Agents(timeout(t))
		if err != nil || len(recordsAfter) != len(recordsBefore) {
			t.Errorf("the registry after the attacks: %d records, %v; before: %d", len(recordsAfter), err, len(recordsBefore))
		}
		if list := h.run(t, h.agentBusctl, "ca", "list"); !strings.Contains(list, "later@carol") {
			t.Errorf("busd no longer serves the owner after the attacks:\n%s", list)
		}
	})

	t.Run("busd serves CONTROL with its own login, which cannot delete, purge or write what it does not own", func(t *testing.T) {
		response, err := http.Get(h.monitorURL + "/connz?auth=true")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var connz struct {
			Connections []struct {
				AuthorizedUser string `json:"authorized_user"`
			} `json:"connections"`
		}
		if err := json.NewDecoder(response.Body).Decode(&connz); err != nil {
			t.Fatal(err)
		}
		served := false
		for _, connection := range connz.Connections {
			served = served || connection.AuthorizedUser == broker.BusdUser
		}
		if !served {
			t.Errorf("no connection of %s: %+v", broker.BusdUser, connz.Connections)
		}

		busdPassword, err := creds.ReadPassword(creds.AdminDir(h.configDir, broker.BusdUser))
		if err != nil {
			t.Fatal(err)
		}
		c := h.connectRaw(t, broker.Connection{User: broker.BusdUser, Password: busdPassword}, "")
		for _, subject := range []string{
			"$JS.API.STREAM.DELETE.LEDGER", "$JS.API.STREAM.PURGE.KV_registry", "$JS.API.STREAM.DELETE.KV_registry",
			"ledger.x", "$JS.API.STREAM.CREATE.X", "dm." + bus.AgentToken(alice),
		} {
			c.denyPublish(t, subject)
		}
		c.expectNoViolation(t, "reading the registry", func() {
			if _, err := c.conn.Request("$JS.API.STREAM.INFO.KV_registry", nil, 2*time.Second); err != nil {
				t.Error(err)
			}
		})
	})

	t.Run("an agent of the bus cannot reach busd's approval API", func(t *testing.T) {
		c := h.rawAgent(t, alice)
		for _, subject := range []string{enroll.SubjectSign, enroll.SubjectList, enroll.SubjectReject, enroll.SubjectRequest, enroll.SubjectPending} {
			c.denyPublish(t, subject)
		}
	})

	t.Run("busd answers only requests: a bare publication on its subjects does nothing", func(t *testing.T) {
		before := h.run(t, h.agentBusctl, "ca", "list")
		admin := h.rawAdmin(t, broker.ControlAdminUser)
		if err := admin.conn.Publish(enroll.SubjectSign, []byte(fmt.Sprintf(`{"ref":%q}`, "later@carol"))); err != nil {
			t.Fatal(err)
		}
		admin.conn.Flush()
		time.Sleep(300 * time.Millisecond)
		if after := h.run(t, h.agentBusctl, "ca", "list"); !strings.Contains(after, "later@carol") || !strings.Contains(before, "later@carol") {
			t.Errorf("a publication without a reply subject approved a request:\n%s", after)
		}
	})

	t.Run("the monitor's admin login reads the pending count and nothing more", func(t *testing.T) {
		counts := make(chan int, 16)
		stop, err := h.admin(t).(*broker.Admin).WatchPendingEnrollments(func(count int) { counts <- count })
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		select {
		case count := <-counts:
			if count < 1 { // guess@carol and later@carol wait
				t.Errorf("busd announced %d pending", count)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("busd never announced the pending count")
		}
	})

	t.Run("the CA key stays in the config directory of busd", func(t *testing.T) {
		info, err := os.Stat(filepath.Join(h.configDir, "ca.key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("ca.key: %v, %v", info, err)
		}
		if _, err := tlsinit.LoadCA(h.caCert, filepath.Join(h.configDir, "ca.key")); err != nil {
			t.Error(err)
		}
	})
}
