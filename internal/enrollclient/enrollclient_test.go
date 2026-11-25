package enrollclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

const agentID = "lib-x@bob"

// fakeBusd answers like busd would for one request: it keeps what it was sent and returns the
// reply the test scripted, with the CA's certificate when it approves.
type fakeBusd struct {
	t       *testing.T
	ca      *tlsinit.CA
	caPEM   []byte
	status  enroll.Status
	message string
	calls   []string // subjects, in order
	sent    []enroll.Request

	// approveAfterPolls turns the status into approved after that many status questions.
	approveAfterPolls int
	// forceID, if set, makes every reply claim to be about that request.
	forceID string
}

func (fake *fakeBusd) Request(_ context.Context, subject string, body []byte) ([]byte, error) {
	fake.calls = append(fake.calls, subject)
	var id string
	switch subject {
	case enroll.SubjectRequest:
		verified, err := enroll.Verify(body)
		if err != nil {
			fake.t.Fatalf("the client sent an invalid request: %v", err)
		}
		fake.sent = append(fake.sent, verified.Request)
		id = verified.ID()
	case enroll.SubjectStatus:
		var query enroll.StatusQuery
		_ = json.Unmarshal(body, &query)
		id = query.RequestID
	}
	reply := enroll.Reply{Status: fake.status, Message: fake.message, RequestID: id}
	if fake.status == enroll.StatusApproved {
		cert, err := fake.ca.SignCSR(broker.AgentCertIdentity(agentID), []byte(fake.sent[len(fake.sent)-1].CSR), time.Hour)
		if err != nil {
			fake.t.Fatal(err)
		}
		reply.Certificate, reply.CA = string(cert), string(fake.caPEM)
		reply.Directory = &enroll.Directory{Channels: map[string][]string{"lib-x": {agentID}}}
	}
	if fake.forceID != "" {
		reply.RequestID = fake.forceID
	}
	if fake.approveAfterPolls > 0 && subject == enroll.SubjectStatus {
		fake.approveAfterPolls--
		if fake.approveAfterPolls == 0 {
			fake.status = enroll.StatusApproved
		}
	}
	return json.Marshal(reply)
}

type fixture struct {
	t         *testing.T
	configDir string
	busd      *fakeBusd
	out       *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	if err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "b.pem"), BrokerKeyPath: filepath.Join(dir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	return &fixture{t: t, configDir: t.TempDir(), out: &bytes.Buffer{}, busd: &fakeBusd{t: t, ca: ca, caPEM: caPEM, status: enroll.StatusPending}}
}

func (f *fixture) run(wait time.Duration) error {
	e := &enrolment{
		opts:      Options{ConfigDir: f.configDir, AgentID: agentID, Broker: "bus.example.org:4222", EnrollPassword: "shared-secret", Wait: wait, Out: f.out},
		requester: f.busd, caPEM: f.busd.caPEM, dir: creds.AgentDir(f.configDir, agentID),
		pollEvery: broker.Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond}, exchangeTimeout: time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.run(ctx)
}

func (f *fixture) agentDir() string { return creds.AgentDir(f.configDir, agentID) }

func TestAPendingRequestIsKeptAndTheNextRunAsksAboutIt(t *testing.T) {
	f := newFixture(t)
	if err := f.run(0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "approval code") || len(f.busd.sent) != 1 {
		t.Fatalf("first run printed %q and sent %d requests", f.out.String(), len(f.busd.sent))
	}
	info, err := os.Stat(filepath.Join(f.agentDir(), pendingFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the pending file: %v, %v", info, err)
	}
	if has, _ := creds.HasClientCert(f.agentDir()); has {
		t.Error("a certificate appeared before approval")
	}

	if err := f.run(0); err != nil {
		t.Fatal(err)
	}
	if len(f.busd.sent) != 1 || f.busd.calls[len(f.busd.calls)-1] != enroll.SubjectStatus {
		t.Errorf("the second run sent %d requests, last call %q: it should only ask for the status", len(f.busd.sent), f.busd.calls[len(f.busd.calls)-1])
	}
}

func TestApprovalInstallsTheCertificateAndTheDirectory(t *testing.T) {
	f := newFixture(t)
	f.busd.status = enroll.StatusApproved
	if err := f.run(0); err != nil {
		t.Fatal(err)
	}
	if has, err := creds.HasClientCert(f.agentDir()); err != nil || !has {
		t.Fatalf("no certificate installed: %v", err)
	}
	certPath, keyPath := creds.ClientCertPaths(f.agentDir())
	if err := creds.ValidateClientCert(certPath, keyPath, broker.AgentCertIdentity(agentID)); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(f.configDir, "directory", "channels")); err != nil {
		t.Errorf("the directory snapshot was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.agentDir(), pendingFile)); err == nil {
		t.Error("the pending file is still there")
	}
	if password, err := creds.ReadPassword(filepath.Join(f.configDir, passwordDir)); err != nil || password != "shared-secret" {
		t.Errorf("the enrollment password kept for the directory fetch: %q, %v", password, err)
	}
	if broker, err := EnrolledBroker(f.configDir, agentID); err != nil || broker != "bus.example.org:4222" {
		t.Errorf("the broker kept as the default: %q, %v", broker, err)
	}
}

func TestEnrolledBrokerIsEmptyBeforeEnrollingAndRefusesAnInvalidFile(t *testing.T) {
	configDir := t.TempDir()
	if broker, err := EnrolledBroker(configDir, agentID); err != nil || broker != "" {
		t.Errorf("before enrolling: %q, %v", broker, err)
	}
	if err := os.MkdirAll(creds.AgentDir(configDir, agentID), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "  \n", "no-port\n", "a b:4222:1"} {
		if err := os.WriteFile(BrokerPath(configDir, agentID), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnrolledBroker(configDir, agentID); err == nil || !strings.Contains(err.Error(), BrokerPath(configDir, agentID)) {
			t.Errorf("file %q: error %v, want one naming the file", content, err)
		}
	}
}

func TestEachIdentityKeepsItsOwnBroker(t *testing.T) {
	configDir := t.TempDir()
	if err := writeBroker(configDir, "a@x", "one:4222"); err != nil {
		t.Fatal(err)
	}
	if err := writeBroker(configDir, "b@x", "two:4222"); err != nil {
		t.Fatal(err)
	}
	if got, _ := EnrolledBroker(configDir, "a@x"); got != "one:4222" {
		t.Errorf("a@x: %q", got)
	}
}

func TestAnExistingIdentityKeepsItsSigningKey(t *testing.T) {
	f := newFixture(t)
	_, key := identity.Generate()
	if err := identity.WritePrivateKey(f.agentDir(), key); err != nil {
		t.Fatal(err)
	}
	if err := f.run(0); err != nil {
		t.Fatal(err)
	}
	kept, err := identity.ReadPrivateKey(f.agentDir())
	if err != nil || !bytes.Equal(kept, key) {
		t.Errorf("the signing key changed: %v", err)
	}
	if got, want := fingerprintOfRequestKey(f.busd.sent[0]), identity.Fingerprint(key.Public().(ed25519.PublicKey)); got != want {
		t.Errorf("the request carries key %s, want the existing %s", got, want)
	}
}

func TestARejectionEndsTheRequestWithItsReason(t *testing.T) {
	f := newFixture(t)
	f.busd.status, f.busd.message = enroll.StatusRejected, "not my laptop"
	err := f.run(0)
	if err == nil || !strings.Contains(err.Error(), "not my laptop") {
		t.Fatalf("run = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.agentDir(), pendingFile)); err == nil {
		t.Error("a rejected request is kept")
	}
	if has, _ := creds.HasClientCert(f.agentDir()); has {
		t.Error("a certificate was installed after a rejection")
	}
}

func TestTextFromBusdIsPrintedWithoutControlCharacters(t *testing.T) {
	f := newFixture(t)
	f.busd.status, f.busd.message = enroll.StatusRejected, "no\x1b[2J\x1b]0;pwned\a\nreally"
	err := f.run(0)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07\n") || !strings.Contains(err.Error(), "really") {
		t.Errorf("a rejection reason with control characters: %q", err)
	}

	f = newFixture(t)
	f.busd.status, f.busd.message, f.busd.forceID = enroll.StatusRejected, "forged\x1b[2J", "someone-else"
	if err := f.run(0); err == nil || strings.ContainsAny(err.Error(), "\x1b") {
		t.Errorf("a refusal with control characters: %q", err)
	}
}

func TestAnAnswerAboutAnotherRequestIsNotBelieved(t *testing.T) {
	f := newFixture(t)
	f.busd.status, f.busd.message, f.busd.forceID = enroll.StatusRejected, "forged", "someone-else"
	if err := f.run(0); err == nil || strings.Contains(err.Error(), "was rejected") {
		t.Fatalf("a forged rejection ended the request: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.agentDir(), pendingFile)); err != nil {
		t.Error("a forged rejection removed the pending request")
	}

	f.busd.status = enroll.StatusApproved
	if err := f.run(0); err == nil {
		t.Fatal("a forged approval was accepted")
	}
	if has, _ := creds.HasClientCert(f.agentDir()); has {
		t.Error("a certificate from a forged approval was installed")
	}
}

func TestWaitingPollsUntilApproved(t *testing.T) {
	f := newFixture(t)
	f.busd.approveAfterPolls = 1
	if err := f.run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if has, _ := creds.HasClientCert(f.agentDir()); !has {
		t.Error("no certificate after waiting")
	}
}

func TestACertificateThatIsNotForThisMachineAndAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	tlsKeyPEM, csrPEM, err := tlsinit.NewClientKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	_, otherCSR, _ := tlsinit.NewClientKeyAndCSR()
	sign := func(identity string, csr []byte, ca *tlsinit.CA) string {
		cert, err := ca.SignCSR(identity, csr, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return string(cert)
	}
	otherDir := t.TempDir()
	if err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(otherDir, "ca.pem"), CAKeyPath: filepath.Join(otherDir, "ca.key"),
		BrokerCertPath: filepath.Join(otherDir, "b.pem"), BrokerKeyPath: filepath.Join(otherDir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	foreignCA, _ := tlsinit.LoadCA(filepath.Join(otherDir, "ca.pem"), filepath.Join(otherDir, "ca.key"))

	good := sign(broker.AgentCertIdentity(agentID), csrPEM, f.busd.ca)
	if err := checkCertificate(good, string(tlsKeyPEM), f.busd.caPEM, agentID); err != nil {
		t.Fatalf("a good certificate was refused: %v", err)
	}
	for name, cert := range map[string]string{
		"another agent's ID":    sign(broker.AgentCertIdentity("api@bob"), csrPEM, f.busd.ca),
		"an admin identity":     sign(broker.AdminCertIdentity(broker.BusAdminUser), csrPEM, f.busd.ca),
		"another key":           sign(broker.AgentCertIdentity(agentID), otherCSR, f.busd.ca),
		"another CA":            sign(broker.AgentCertIdentity(agentID), csrPEM, foreignCA),
		"two certificates":      good + good,
		"no certificate at all": "",
	} {
		if err := checkCertificate(cert, string(tlsKeyPEM), f.busd.caPEM, agentID); err == nil {
			t.Errorf("a certificate for %s was accepted", name)
		}
	}
}

func TestTheCAComesFromTheOwnersFingerprintNotFromTheBroker(t *testing.T) {
	f := newFixture(t)
	caPath := filepath.Join(f.configDir, "ca.pem")
	if err := os.WriteFile(caPath, f.busd.caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{CACertPath: caPath, Out: f.out}

	if _, err := trustedCA(caPath, opts); err != nil {
		t.Errorf("a copied CA without a fingerprint: %v", err)
	}
	fingerprint, _ := tlsinit.FingerprintOfPEM(f.busd.caPEM)
	opts.CAFingerprint = strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	if _, err := trustedCA(caPath, opts); err != nil {
		t.Errorf("a copied CA with its fingerprint: %v", err)
	}
	opts.CAFingerprint = strings.Repeat("00", 32)
	if _, err := trustedCA(caPath, opts); err == nil {
		t.Error("a copied CA that does not match the owner's fingerprint was trusted")
	}
	// Neither a CA nor a fingerprint: nothing may be learned from the broker.
	opts.CAFingerprint = ""
	if _, err := trustedCA(filepath.Join(f.configDir, "none.pem"), opts); err == nil || !strings.Contains(err.Error(), "--ca-fingerprint") {
		t.Errorf("without a CA or a fingerprint: %v", err)
	}
}
