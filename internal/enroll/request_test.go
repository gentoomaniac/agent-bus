package enroll

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

func newTestRequest(t *testing.T, agentID string) (Request, ed25519.PrivateKey) {
	t.Helper()
	_, signingKey := identity.Generate()
	_, csrPEM, err := tlsinit.NewClientKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(agentID, signingKey, csrPEM, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	return request, signingKey
}

func encode(t *testing.T, request Request) []byte {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyAcceptsAFreshRequest(t *testing.T) {
	request, signingKey := newTestRequest(t, "lib-x@bob")
	verified, err := Verify(encode(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if verified.AgentID != "lib-x@bob" || verified.SigningFingerprint != identity.Fingerprint(signingKey.Public().(ed25519.PublicKey)) {
		t.Errorf("verified %+v", verified)
	}
	if verified.TLSFingerprint == "" || verified.TLSFingerprint == verified.SigningFingerprint {
		t.Errorf("TLS fingerprint %q", verified.TLSFingerprint)
	}
}

func TestVerifyRefusesAnyChangedField(t *testing.T) {
	request, _ := newTestRequest(t, "lib-x@bob")
	other, _ := newTestRequest(t, "lib-x@bob")
	for name, change := range map[string]func(*Request){
		"agent ID":       func(r *Request) { r.AgentID = "api-client@bob" },
		"CSR":            func(r *Request) { r.CSR = other.CSR },
		"signing key":    func(r *Request) { r.PublicKey = other.PublicKey },
		"hostname":       func(r *Request) { r.Hostname = "elsewhere" },
		"nonce":          func(r *Request) { r.Nonce = other.Nonce },
		"proof":          func(r *Request) { r.Proof = other.Proof },
		"another's pair": func(r *Request) { r.PublicKey, r.Proof = other.PublicKey, other.Proof },
	} {
		changed := request
		change(&changed)
		if _, err := Verify(encode(t, changed)); err == nil {
			t.Errorf("a request with a changed %s verified", name)
		}
	}
}

func TestVerifyDoesNotCoverTheSecret(t *testing.T) {
	request, _ := newTestRequest(t, "lib-x@bob")
	id, code := request.ID(), request.Code()
	request.Secret = "an autosign secret"
	if _, err := Verify(encode(t, request)); err != nil {
		t.Fatal(err)
	}
	if request.ID() != id || request.Code() != code {
		t.Error("the secret changes the approval code")
	}
}

func TestVerifyRefusesMalformedRequests(t *testing.T) {
	request, _ := newTestRequest(t, "lib-x@bob")
	withBadField := func(change func(*Request)) []byte {
		changed := request
		change(&changed)
		return encode(t, changed)
	}
	for name, data := range map[string][]byte{
		"not JSON":            []byte("hello"),
		"unknown field":       []byte(`{"agent_id":"lib-x@bob","admin":true}`),
		"too large":           append([]byte(`{"agent_id":"`), []byte(strings.Repeat("a", MaxRequestBytes))...),
		"invalid agent ID":    withBadField(func(r *Request) { r.AgentID = "Not An ID" }),
		"human agent":         withBadField(func(r *Request) { r.AgentID = "human@bob" }),
		"empty hostname":      withBadField(func(r *Request) { r.Hostname = "" }),
		"control characters":  withBadField(func(r *Request) { r.Hostname = "evil\x1b[2J" }),
		"huge hostname":       withBadField(func(r *Request) { r.Hostname = strings.Repeat("h", MaxHostnameBytes+1) }),
		"huge secret":         withBadField(func(r *Request) { r.Secret = strings.Repeat("s", MaxSecretBytes+1) }),
		"short nonce":         withBadField(func(r *Request) { r.Nonce = "AAAA" }),
		"short signing key":   withBadField(func(r *Request) { r.PublicKey = "AAAA" }),
		"CSR of another type": withBadField(func(r *Request) { r.CSR = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n" }),
	} {
		if _, err := Verify(data); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestVerifyRefusesACSRThatIsNotSignedByItsKey(t *testing.T) {
	request, signingKey := newTestRequest(t, "lib-x@bob")
	// Flip a byte of the CSR's signature, then sign the whole thing properly.
	tampered := []byte(request.CSR)
	for i := len(tampered) - 40; i > 0; i-- {
		if tampered[i] >= 'A' && tampered[i] < 'Z' {
			tampered[i]++
			break
		}
	}
	request.CSR = string(tampered)
	digest := request.digest()
	request.Proof = base64.StdEncoding.EncodeToString(ed25519.Sign(signingKey, digest[:]))
	if _, err := Verify(encode(t, request)); err == nil {
		t.Error("a CSR with a broken signature verified")
	}
}

func TestIDAndCodeDependOnEveryCoveredField(t *testing.T) {
	request, _ := newTestRequest(t, "lib-x@bob")
	again := request
	if again.ID() != request.ID() || again.Code() != request.Code() {
		t.Fatal("the same request has two codes")
	}
	seen := map[string]string{request.Code(): "original"}
	for name, change := range map[string]func(*Request){
		"agent ID": func(r *Request) { r.AgentID = "lib-y@bob" },
		"CSR":      func(r *Request) { r.CSR += " " },
		"key":      func(r *Request) { r.PublicKey = "x" },
		"hostname": func(r *Request) { r.Hostname = "other" },
		"nonce":    func(r *Request) { r.Nonce = "y" },
	} {
		changed := request
		change(&changed)
		if changed.ID() == request.ID() {
			t.Errorf("the ID ignores the %s", name)
		}
		if previous, clash := seen[changed.Code()]; clash {
			t.Errorf("changing the %s gives the code of %s", name, previous)
		}
		seen[changed.Code()] = name
	}
	if code := request.Code(); len(code) != 9 || code[4] != '-' {
		t.Errorf("code %q is not XXXX-XXXX", code)
	}
}

func TestFieldsCannotShiftIntoEachOther(t *testing.T) {
	a := Request{AgentID: "a@b", Hostname: "xy", Nonce: "z"}
	b := Request{AgentID: "a@b", Hostname: "x", Nonce: "yz"}
	if a.ID() == b.ID() {
		t.Error("moving a byte between fields keeps the digest")
	}
}

func TestEveryRequestHasAFreshNonce(t *testing.T) {
	_, key := identity.Generate()
	_, csr, _ := tlsinit.NewClientKeyAndCSR()
	first, _ := NewRequest("lib-x@bob", key, csr, "h", "")
	second, _ := NewRequest("lib-x@bob", key, csr, "h", "")
	if first.Nonce == second.Nonce || first.ID() == second.ID() {
		t.Error("two requests of one machine are the same request")
	}
}
