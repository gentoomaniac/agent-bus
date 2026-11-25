package busd

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// fakeRegistry records what approval does to the broker.
type fakeRegistry struct {
	records  map[string]bus.AgentRecord
	accounts []string
	certIDs  map[string]string // the certificate each account logs in with
	cursors  []string
	last     uint64 // the sequence LastSequence reports
	down     error
}

func (fake *fakeRegistry) Agents(context.Context) ([]bus.AgentRecord, error) {
	var records []bus.AgentRecord
	for _, record := range fake.records {
		records = append(records, record)
	}
	return records, fake.down
}
func (fake *fakeRegistry) PutAgentRecord(_ context.Context, record bus.AgentRecord) error {
	fake.records[record.AgentID] = record
	return nil
}
func (fake *fakeRegistry) CreateReadCursor(_ context.Context, agentID string, channel bus.Channel, afterSequence uint64) error {
	fake.cursors = append(fake.cursors, fmt.Sprintf("%s %s after %d", agentID, channel, afterSequence))
	return nil
}
func (fake *fakeRegistry) LastSequence(context.Context) (uint64, error) { return fake.last, nil }
func (fake *fakeRegistry) EnsureCertAccount(_ context.Context, agentID, certID string) error {
	fake.accounts = append(fake.accounts, agentID)
	fake.certIDs[agentID] = certID
	return nil
}
func (*fakeRegistry) GroupChannels(context.Context) ([]bus.TopicRecord, error) { return nil, nil }
func (*fakeRegistry) ReadCursorHolders(context.Context, bus.Channel) ([]string, error) {
	return nil, nil
}

type fixture struct {
	t        *testing.T
	service  *Service
	registry *fakeRegistry
	dir      string
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "broker.pem"), BrokerKeyPath: filepath.Join(dir, "broker.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	f := &fixture{t: t, registry: &fakeRegistry{records: map[string]bus.AgentRecord{}, certIDs: map[string]string{}}, dir: dir, now: time.Now()}
	f.service, err = New(Config{Registry: f.registry, CA: ca, CACertPEM: caPEM, ConfigDir: dir, CertValidity: time.Hour, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.service.limiter = rate.NewLimiter(rate.Inf, 1) // limits are tested on their own
	return f
}

func (f *fixture) restart() {
	f.t.Helper()
	service, err := New(f.service.config)
	if err != nil {
		f.t.Fatal(err)
	}
	service.limiter = f.service.limiter
	f.service = service
}

func (f *fixture) submit(agentID string, signingKey ed25519.PrivateKey, secret string) enroll.Reply {
	f.t.Helper()
	_, csr, err := tlsinit.NewClientKeyAndCSR()
	if err != nil {
		f.t.Fatal(err)
	}
	return f.submitCSR(agentID, signingKey, csr, secret)
}

func (f *fixture) submitCSR(agentID string, signingKey ed25519.PrivateKey, csr []byte, secret string) enroll.Reply {
	f.t.Helper()
	request, err := enroll.NewRequest(agentID, signingKey, csr, "laptop", secret)
	if err != nil {
		f.t.Fatal(err)
	}
	data, _ := json.Marshal(request)
	return f.service.HandleRequest(context.Background(), data)
}

func (f *fixture) newKey() ed25519.PrivateKey {
	_, key := identity.Generate()
	return key
}

func (f *fixture) register(agentID string, key ed25519.PrivateKey) {
	public := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	f.registry.records[agentID] = bus.AgentRecord{AgentID: agentID, PublicKey: public, Fingerprint: identity.Fingerprint(key.Public().(ed25519.PublicKey)),
		Keys: []bus.KeyEntry{{PublicKey: public, ValidFrom: f.now}}}
}

func (f *fixture) sign(ref string, replace bool) enroll.CommandReply {
	return f.service.Sign(context.Background(), enroll.SignCommand{Ref: ref, Replace: replace})
}

func (f *fixture) status(id string) enroll.Reply {
	data, _ := json.Marshal(enroll.StatusQuery{RequestID: id})
	return f.service.HandleStatus(context.Background(), data)
}

func parseCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestRequestWaitsForTheOwnerAndSigningIssuesTheCertificate(t *testing.T) {
	f := newFixture(t)
	f.registry.last = 42
	pending := f.submit("lib-x@bob", f.newKey(), "")
	if pending.Status != enroll.StatusPending || pending.Code == "" || pending.Certificate != "" {
		t.Fatalf("first reply %+v", pending)
	}
	if f.service.PendingCount() != 1 || len(f.registry.accounts) != 0 {
		t.Fatal("a request alone changed the broker")
	}
	list := f.service.List(context.Background()).Pending
	if len(list) != 1 || list[0].Identity != enroll.IdentityNew || list[0].Code != pending.Code || list[0].Hostname != "laptop" {
		t.Fatalf("list %+v", list)
	}

	if reply := f.sign(pending.Code, false); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	approved := f.status(pending.RequestID)
	if approved.Status != enroll.StatusApproved || approved.Certificate == "" || approved.CA == "" || approved.Directory == nil {
		t.Fatalf("status after signing %+v", approved)
	}
	cert := parseCert(t, approved.Certificate)
	certID := f.registry.certIDs["lib-x@bob"]
	if len(cert.URIs) != 1 || certID == "" || cert.URIs[0].String() != "agentbus:agent/lib-x_bob/"+certID {
		t.Errorf("certificate URIs %v, the account logs in with certificate %q", cert.URIs, certID)
	}
	if len(f.registry.accounts) != 1 || f.registry.accounts[0] != "lib-x@bob" || !slices.Equal(f.registry.cursors, []string{"lib-x@bob DM lib-x@bob after 42", "lib-x@bob general after 42"}) {
		t.Errorf("accounts %v, cursors %v, want both after the log's end", f.registry.accounts, f.registry.cursors)
	}
	record := f.registry.records["lib-x@bob"]
	if len(record.Keys) != 1 || record.PublicKey == "" || len(approved.Directory.Records) != 1 {
		t.Errorf("registry record %+v", record)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "directory", "lib-x_bob.json")); err != nil {
		t.Errorf("busd's own directory copy: %v", err)
	}
	if f.service.PendingCount() != 0 {
		t.Error("an approved request is still pending")
	}
}

func TestACertificateIsBuiltFromTheApprovedIDNotFromTheCSR(t *testing.T) {
	f := newFixture(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	adminURI, _ := url.Parse("agentbus:admin/bus-admin")
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "agentbus:admin/bus-admin", Organization: []string{"CA"}},
		DNSNames: []string{"broker"}, URIs: []*url.URL{adminURI}, EmailAddresses: []string{"root@x"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	reply := f.submitCSR("lib-x@bob", f.newKey(), csr, "")
	if r := f.sign(reply.Code, false); r.Error != "" {
		t.Fatal(r.Error)
	}
	cert := parseCert(t, f.status(reply.RequestID).Certificate)
	want := "agentbus:agent/lib-x_bob/" + f.registry.certIDs["lib-x@bob"]
	if len(cert.URIs) != 1 || cert.URIs[0].String() != want ||
		len(cert.DNSNames) != 0 || len(cert.EmailAddresses) != 0 || cert.IsCA ||
		cert.Subject.CommonName != want || len(cert.Subject.Organization) != 0 {
		t.Errorf("the certificate took something from the CSR: %+v", cert)
	}
	if cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(cert.ExtKeyUsage) != 1 {
		t.Errorf("usages %v", cert.ExtKeyUsage)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		t.Error("the certificate is not for the CSR's key")
	}
}

func TestApprovalIsSingleUse(t *testing.T) {
	f := newFixture(t)
	reply := f.submit("lib-x@bob", f.newKey(), "")
	if r := f.sign(reply.Code, false); r.Error != "" {
		t.Fatal(r.Error)
	}
	for _, ref := range []string{reply.Code, reply.RequestID, "lib-x@bob"} {
		if r := f.sign(ref, true); r.Error == "" {
			t.Errorf("signing %q again was accepted", ref)
		}
	}
	if rejected := f.service.Reject(enroll.RejectCommand{Ref: reply.Code}); rejected.Error == "" {
		t.Error("an approved request was rejected afterwards")
	}
	if len(f.registry.accounts) != 1 {
		t.Errorf("accounts changed %d times", len(f.registry.accounts))
	}
}

func TestSigningNamesTheRequestByIDCodeOrAgentID(t *testing.T) {
	f := newFixture(t)
	first := f.submit("lib-x@bob", f.newKey(), "")
	second := f.submit("api@bob", f.newKey(), "")
	if r := f.sign(strings.ToLower(strings.ReplaceAll(first.Code, "-", "")), false); r.Error != "" {
		t.Errorf("code without dash, lower case: %s", r.Error)
	}
	if r := f.sign("api@bob", false); r.Error != "" {
		t.Errorf("agent ID: %s", r.Error)
	}
	if f.status(second.RequestID).Status != enroll.StatusApproved {
		t.Error("the second request was not approved")
	}
	if r := f.sign("nobody@bob", false); r.Error == "" {
		t.Error("signed a request that does not exist")
	}
}

func TestAnAgentIDWithTwoRequestsMustBeNamedByCode(t *testing.T) {
	f := newFixture(t)
	f.submit("lib-x@bob", f.newKey(), "")
	f.submit("lib-x@bob", f.newKey(), "")
	r := f.sign("lib-x@bob", false)
	if !strings.Contains(r.Error, "name one by its code") {
		t.Errorf("ambiguous ID: %q", r.Error)
	}
	if len(f.registry.accounts) != 0 {
		t.Error("an ambiguous reference approved something")
	}
}

func TestRejectionTellsTheAgentWhy(t *testing.T) {
	f := newFixture(t)
	reply := f.submit("lib-x@bob", f.newKey(), "")
	if r := f.service.Reject(enroll.RejectCommand{Ref: reply.Code, Reason: "unknown machine\x1b[2J"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	got := f.status(reply.RequestID)
	if got.Status != enroll.StatusRejected || got.Message != "unknown machine[2J" || got.Certificate != "" {
		t.Errorf("status after rejection %+v", got)
	}
	if f.service.PendingCount() != 0 || len(f.registry.accounts) != 0 {
		t.Error("rejecting changed the broker or left the request pending")
	}
}

func TestARemovedIDIsRefusedAtRequestAndAtApproval(t *testing.T) {
	f := newFixture(t)
	key := f.newKey()
	f.register("old@bob", key)
	waiting := f.submit("old@bob", key, "")
	removedAt := f.now
	record := f.registry.records["old@bob"]
	record.RemovedAt = &removedAt
	f.registry.records["old@bob"] = record

	if r := f.sign(waiting.Code, true); !strings.Contains(r.Error, "removed") {
		t.Errorf("approving a removed ID: %q", r.Error)
	}
	if again := f.submit("old@bob", f.newKey(), ""); again.Status != enroll.StatusRejected || !strings.Contains(again.Message, "removed") {
		t.Errorf("a request for a removed ID: %+v", again)
	}
	if len(f.registry.accounts) != 0 {
		t.Error("an account was made for a removed ID")
	}
}

func TestAnExistingIDWithAnotherKeyNeedsExplicitReplacement(t *testing.T) {
	f := newFixture(t)
	f.register("lib-x@bob", f.newKey())
	reply := f.submit("lib-x@bob", f.newKey(), "")
	if list := f.service.List(context.Background()).Pending; list[0].Identity != enroll.IdentityReplaces {
		t.Fatalf("identity %q", list[0].Identity)
	}
	if r := f.sign(reply.Code, false); !strings.Contains(r.Error, "--replace") {
		t.Fatalf("signing without confirmation: %q", r.Error)
	}
	if len(f.registry.accounts) != 0 || f.status(reply.RequestID).Status != enroll.StatusPending {
		t.Fatal("a refused approval changed something")
	}

	oldKey := f.registry.records["lib-x@bob"].PublicKey
	if r := f.sign(reply.Code, true); r.Error != "" {
		t.Fatal(r.Error)
	}
	record := f.registry.records["lib-x@bob"]
	if len(record.Keys) != 2 || !record.Keys[0].Revoked || record.Keys[1].Revoked || record.PublicKey == oldKey || record.Keys[0].PublicKey != oldKey {
		t.Errorf("key history after replacement: %+v", record.Keys)
	}
}

func TestAnExistingIDWithTheSameKeyOnlySwitchesToACertificate(t *testing.T) {
	f := newFixture(t)
	key := f.newKey()
	f.register("lib-x@bob", key)
	before := f.registry.records["lib-x@bob"]
	reply := f.submit("lib-x@bob", key, "")
	if list := f.service.List(context.Background()).Pending; list[0].Identity != enroll.IdentitySameKey {
		t.Fatalf("identity %q", list[0].Identity)
	}
	if r := f.sign(reply.Code, false); r.Error != "" {
		t.Fatal(r.Error)
	}
	if got := f.registry.records["lib-x@bob"]; got.Keys[0] != before.Keys[0] || len(got.Keys) != 1 {
		t.Errorf("the registry record changed: %+v", got)
	}
	if len(f.registry.accounts) != 1 {
		t.Error("the account was not switched")
	}
}

func TestPendingRequestsAreLimited(t *testing.T) {
	f := newFixture(t)
	for i := range maxPendingPerAgent {
		if reply := f.submit("lib-x@bob", f.newKey(), ""); reply.Status != enroll.StatusPending {
			t.Fatalf("request %d: %+v", i, reply)
		}
	}
	if reply := f.submit("lib-x@bob", f.newKey(), ""); reply.Status != enroll.StatusBusy {
		t.Errorf("a request over the per-ID limit: %+v", reply)
	}
	for i := 0; f.service.PendingCount() < maxPending; i++ {
		f.submit("agent-"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@bob", f.newKey(), "")
	}
	if reply := f.submit("late@bob", f.newKey(), ""); reply.Status != enroll.StatusBusy {
		t.Errorf("a request over the pending limit: %+v", reply)
	}
}

func TestRequestsAreRateLimited(t *testing.T) {
	f := newFixture(t)
	f.service.limiter = rate.NewLimiter(rate.Every(time.Hour), 2)
	statuses := []enroll.Status{}
	for range 4 {
		statuses = append(statuses, f.submit("lib-x@bob", f.newKey(), "").Status)
	}
	if statuses[0] != enroll.StatusPending || statuses[2] != enroll.StatusBusy || statuses[3] != enroll.StatusBusy {
		t.Errorf("statuses %v", statuses)
	}
}

func TestAnAgentThatAsksAgainSupersedesItsOwnRequest(t *testing.T) {
	f := newFixture(t)
	key := f.newKey()
	first := f.submit("lib-x@bob", key, "")
	second := f.submit("lib-x@bob", key, "")
	if first.RequestID == second.RequestID || f.service.PendingCount() != 1 {
		t.Fatalf("pending %d", f.service.PendingCount())
	}
	if f.status(first.RequestID).Status != enroll.StatusUnknown {
		t.Error("the superseded request is still known")
	}
}

func TestTheSameRequestAgainGetsTheSameAnswer(t *testing.T) {
	f := newFixture(t)
	_, csr, _ := tlsinit.NewClientKeyAndCSR()
	key := f.newKey()
	request, _ := enroll.NewRequest("lib-x@bob", key, csr, "laptop", "")
	data, _ := json.Marshal(request)
	first := f.service.HandleRequest(context.Background(), data)
	again := f.service.HandleRequest(context.Background(), data)
	if first.RequestID != again.RequestID || f.service.PendingCount() != 1 {
		t.Errorf("a resend made a second request: %+v", again)
	}
}

func TestRequestsExpireAndTheAnswerIsKeptForTheAgent(t *testing.T) {
	f := newFixture(t)
	waiting := f.submit("lib-x@bob", f.newKey(), "")
	f.now = f.now.Add(requestLifetime + time.Minute)
	expired := f.status(waiting.RequestID)
	if expired.Status != enroll.StatusRejected || !strings.Contains(expired.Message, "expired") {
		t.Errorf("an expired request: %+v", expired)
	}
	if f.service.PendingCount() != 0 {
		t.Error("an expired request is still pending")
	}
	if r := f.sign(waiting.Code, false); r.Error == "" {
		t.Error("an expired request was approved")
	}
	f.now = f.now.Add(requestLifetime + time.Minute)
	if f.status(waiting.RequestID).Status != enroll.StatusUnknown {
		t.Error("the decided request is never forgotten")
	}
}

func TestMalformedRequestsAreRejectedWithoutBeingStored(t *testing.T) {
	f := newFixture(t)
	for name, data := range map[string][]byte{
		"garbage":   []byte("hello"),
		"too large": []byte(strings.Repeat("x", enroll.MaxRequestBytes+1)),
	} {
		if reply := f.service.HandleRequest(context.Background(), data); reply.Status != enroll.StatusRejected {
			t.Errorf("%s: %+v", name, reply)
		}
	}
	forged, _ := enroll.NewRequest("lib-x@bob", f.newKey(), []byte("x"), "laptop", "")
	forged.AgentID = "api@bob"
	data, _ := json.Marshal(forged)
	if reply := f.service.HandleRequest(context.Background(), data); reply.Status != enroll.StatusRejected {
		t.Errorf("a forged request: %+v", reply)
	}
	if got := f.service.HandleStatus(context.Background(), []byte("{")); got.Status != enroll.StatusRejected {
		t.Errorf("a garbled status query: %+v", got)
	}
	if f.service.PendingCount() != 0 {
		t.Error("a malformed request was stored")
	}
}

func TestAutosign(t *testing.T) {
	f := newFixture(t)
	f.service.limiter = rate.NewLimiter(rate.Inf, 1)
	secretPath := filepath.Join(f.dir, "enroll", "autosign")

	if reply := f.submit("off@bob", f.newKey(), "anything"); reply.Status != enroll.StatusPending {
		t.Errorf("autosign is off but %+v", reply)
	}
	if err := os.MkdirAll(filepath.Dir(secretPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte("sesame\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if reply := f.submit("wrong@bob", f.newKey(), "open"); reply.Status != enroll.StatusPending {
		t.Errorf("a wrong secret: %+v", reply)
	}
	if reply := f.submit("none@bob", f.newKey(), ""); reply.Status != enroll.StatusPending {
		t.Errorf("no secret: %+v", reply)
	}
	good := f.submit("good@bob", f.newKey(), "sesame")
	if good.Status != enroll.StatusApproved || good.Certificate == "" {
		t.Fatalf("the right secret: %+v", good)
	}
	if !strings.Contains(string(mustRead(t, f.service.path(stateFile))), `"agent_id":"good@bob"`) {
		t.Error("the autosigned request is not in the state")
	}
	if strings.Contains(string(mustRead(t, f.service.path(stateFile))), "sesame") {
		t.Error("the autosign secret was written to the state file")
	}

	// It never replaces an identity with another key, with or without the secret.
	f.register("held@bob", f.newKey())
	if reply := f.submit("held@bob", f.newKey(), "sesame"); reply.Status != enroll.StatusPending {
		t.Errorf("autosign replaced an identity: %+v", reply)
	}
	// An empty file is off, not "any secret".
	if err := os.WriteFile(secretPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if reply := f.submit("empty@bob", f.newKey(), ""); reply.Status != enroll.StatusPending {
		t.Errorf("an empty secret file: %+v", reply)
	}
}

func TestAnUnreadableRegistryMakesRequestsBusyNotApproved(t *testing.T) {
	f := newFixture(t)
	f.registry.down = os.ErrDeadlineExceeded
	if reply := f.submit("lib-x@bob", f.newKey(), ""); reply.Status != enroll.StatusBusy {
		t.Errorf("with the registry down: %+v", reply)
	}
}

func TestPendingAndApprovedRequestsSurviveARestart(t *testing.T) {
	f := newFixture(t)
	waiting := f.submit("wait@bob", f.newKey(), "")
	done := f.submit("done@bob", f.newKey(), "")
	if r := f.sign(done.Code, false); r.Error != "" {
		t.Fatal(r.Error)
	}
	f.restart()
	if f.service.PendingCount() != 1 || f.status(waiting.RequestID).Code != waiting.Code {
		t.Errorf("the pending request was lost")
	}
	if got := f.status(done.RequestID); got.Status != enroll.StatusApproved || got.Certificate == "" {
		t.Errorf("the approved request after a restart: %+v", got)
	}
	if r := f.sign(waiting.Code, false); r.Error != "" {
		t.Errorf("signing after a restart: %s", r.Error)
	}
	info, err := os.Stat(f.service.path(stateFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v, %v", info.Mode().Perm(), err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A failure after the registry took the new key leaves the old key in the directory file, and a
// repeated approval sees the registry already holding the new key.
func TestARepeatedReplacementRewritesTheDirectoryEntry(t *testing.T) {
	f := newFixture(t)
	oldKey, newKey := f.newKey(), f.newKey()
	f.register("lib-x@bob", oldKey)
	directory := filepath.Join(f.dir, "directory")
	if err := os.WriteFile(directory, []byte("in the way"), 0o600); err != nil { // the directory cannot be written
		t.Fatal(err)
	}
	reply := f.submit("lib-x@bob", newKey, "")
	if r := f.sign(reply.Code, true); r.Error == "" {
		t.Fatal("the approval succeeded although the directory could not be written")
	}
	if f.service.PendingCount() != 1 {
		t.Fatal("the failed approval is no longer pending")
	}

	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if r := f.sign(reply.Code, false); r.Error != "" { // the registry has the new key now: no --replace needed
		t.Fatal(r.Error)
	}
	data, err := os.ReadFile(filepath.Join(directory, "lib-x_bob.json"))
	if err != nil {
		t.Fatal(err)
	}
	var written bus.AgentRecord
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	wantKey := base64.StdEncoding.EncodeToString(newKey.Public().(ed25519.PublicKey))
	if written.PublicKey != wantKey || len(written.Keys) != 2 || !written.Keys[0].Revoked {
		t.Errorf("the directory entry after the repeat: %+v, want the new key %s", written, wantKey)
	}
}
