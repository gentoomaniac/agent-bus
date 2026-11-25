// Package enroll is the wire protocol of Puppet-style agent enrollment (docs/design/access-control.md): the
// request an agent machine sends, how busd checks it, the approval code the owner compares, and
// the replies. It touches no broker: busd (internal/busd) serves it, `agent-bus enroll`
// (internal/enrollclient) and `agent-busctl ca` speak it.
//
// The enrollment login is shared, so nothing here is secret on the way back: replies carry public
// data only (certificate, CA certificate, directory), never a key.
package enroll

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

// The subjects of busd's API, all in the CONTROL account. The enrollment login may publish
// SubjectRequest, SubjectStatus and SubjectDirectory; the rest need an admin login. busd publishes
// the number of pending requests on SubjectPending, for the monitor, which reads the registry and
// the catalogue with requests on the last two: its login has no JetStream access in CONTROL.
const (
	SubjectRequest = "busd.enroll.request"
	SubjectStatus  = "busd.enroll.status"
	// SubjectDirectory answers an enrolled agent that keeps its local directory up to date.
	SubjectDirectory = "busd.directory"
	SubjectPending   = "busd.enroll.pending"
	SubjectList      = "busd.ca.list"
	SubjectSign      = "busd.ca.sign"
	SubjectReject    = "busd.ca.reject"

	SubjectMonitorRegistry  = "busd.monitor.registry"
	SubjectMonitorCatalogue = "busd.monitor.catalogue"
)

// EnrollUser is the shared login of the enrollment listener; InboxPrefix is the only subject
// space it may subscribe to, with one random sub-prefix per client. AdminInboxPrefix is where the
// owner's tool (agent-busctl) and MonitorInboxPrefix the monitor hear busd's answers. busd answers a request only below the prefix
// of its endpoint, because the sender names the reply subject.
const (
	EnrollUser         = "enroll"
	InboxPrefix        = "_INBOX.enroll"
	AdminInboxPrefix   = "_INBOX.agent-busctl"
	MonitorInboxPrefix = "_INBOX.bus-monitor"
)

// Bounds on what busd accepts from the shared login.
const (
	MaxRequestBytes  = 8 << 10
	MaxSecretBytes   = 256
	MaxHostnameBytes = 253
	nonceBytes       = 16
	codeBytes        = 5 // 40 bits, shown as XXXX-XXXX
)

// proofDomain separates the proof of possession from every other signature of the bus.
const proofDomain = "agent-bus/enroll-request/v1"

// Request is what an agent machine sends to busd. Everything but Secret is covered by Proof and
// by the approval code, so the owner approves exactly this identity, key and certificate request.
type Request struct {
	AgentID   string `json:"agent_id"`
	CSR       string `json:"csr"`        // PEM; only its public key is used
	PublicKey string `json:"public_key"` // base64 of the raw Ed25519 signing key
	Hostname  string `json:"hostname"`
	Nonce     string `json:"nonce"`  // base64, fresh per request
	Proof     string `json:"proof"`  // base64 Ed25519 signature of the digest, by the signing key
	Secret    string `json:"secret"` // the autosign secret, if the owner gave one; not part of the code
}

// NewRequest builds and signs a request. The signing key is the agent's identity key.
func NewRequest(agentID string, signingKey ed25519.PrivateKey, csrPEM []byte, hostname, secret string) (Request, error) {
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return Request{}, err
	}
	request := Request{
		AgentID:   agentID,
		CSR:       string(csrPEM),
		PublicKey: base64.StdEncoding.EncodeToString(signingKey.Public().(ed25519.PublicKey)),
		Hostname:  hostname,
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
		Secret:    secret,
	}
	digest := request.digest()
	request.Proof = base64.StdEncoding.EncodeToString(ed25519.Sign(signingKey, digest[:]))
	return request, nil
}

// digest hashes the whole request but the proof and the secret. Fields are length-prefixed, so no
// two different requests share a digest by moving a byte from one field to the next.
func (request Request) digest() [sha256.Size]byte {
	var framed bytes.Buffer
	framed.WriteString(proofDomain)
	for _, field := range []string{request.AgentID, request.CSR, request.PublicKey, request.Hostname, request.Nonce} {
		_ = binary.Write(&framed, binary.BigEndian, uint32(len(field)))
		framed.WriteString(field)
	}
	return sha256.Sum256(framed.Bytes())
}

// ID names the request: the hex digest. A client asks for the state of its request by ID.
func (request Request) ID() string {
	digest := request.digest()
	return hex.EncodeToString(digest[:])
}

// Code is the short form of the digest that the owner compares: the agent machine prints it, the
// approval screen shows it. Forty bits make a second request with the same code impractical to
// find, and the owner also sees the agent ID and the key fingerprints.
func (request Request) Code() string {
	digest := request.digest()
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:codeBytes])
	return encoded[:4] + "-" + encoded[4:]
}

// Verified is a request that passed Verify, with what busd needs from it.
type Verified struct {
	Request
	SigningKey         ed25519.PublicKey
	TLSKey             crypto.PublicKey
	SigningFingerprint string
	TLSFingerprint     string
}

// Verify checks a request as received from the enrollment login, which proves nothing about the
// sender: size, strict JSON, a valid agent ID, a well-formed nonce, a printable hostname, a
// certificate request signed by its own key, and a proof of possession of the signing key over
// the whole request.
func Verify(data []byte) (Verified, error) {
	if len(data) > MaxRequestBytes {
		return Verified{}, fmt.Errorf("the request is %d bytes, at most %d are accepted", len(data), MaxRequestBytes)
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Verified{}, fmt.Errorf("the request is not valid JSON of the enrollment format: %w", err)
	}
	if err := bus.ValidateAgentID(request.AgentID); err != nil {
		return Verified{}, err
	}
	if strings.HasPrefix(request.AgentID, "human@") {
		return Verified{}, fmt.Errorf("%s needs a password-protected key, which enrollment does not support", request.AgentID)
	}
	if err := validateText("hostname", request.Hostname, MaxHostnameBytes); err != nil {
		return Verified{}, err
	}
	if len(request.Secret) > MaxSecretBytes {
		return Verified{}, errors.New("the secret is too long")
	}
	nonce, err := base64.StdEncoding.DecodeString(request.Nonce)
	if err != nil || len(nonce) != nonceBytes {
		return Verified{}, fmt.Errorf("the nonce must be %d random bytes in base64", nonceBytes)
	}
	signingKey, err := base64.StdEncoding.DecodeString(request.PublicKey)
	if err != nil || len(signingKey) != ed25519.PublicKeySize {
		return Verified{}, errors.New("the signing key must be a raw Ed25519 public key in base64")
	}
	proof, err := base64.StdEncoding.DecodeString(request.Proof)
	if err != nil {
		return Verified{}, errors.New("the proof is not base64")
	}
	digest := request.digest()
	if !ed25519.Verify(signingKey, digest[:], proof) {
		return Verified{}, errors.New("the proof of possession does not match the signing key")
	}
	tlsKey, err := publicKeyOfCSR(request.CSR)
	if err != nil {
		return Verified{}, err
	}
	tlsDER, err := x509.MarshalPKIXPublicKey(tlsKey)
	if err != nil {
		return Verified{}, err
	}
	return Verified{
		Request:            request,
		SigningKey:         signingKey,
		TLSKey:             tlsKey,
		SigningFingerprint: identity.Fingerprint(signingKey),
		TLSFingerprint:     identity.Fingerprint(tlsDER),
	}, nil
}

// validateText accepts printable text only, so the owner's screen shows what was sent.
func validateText(name, text string, maxBytes int) error {
	if text == "" || len(text) > maxBytes || !utf8.ValidString(text) {
		return fmt.Errorf("the %s must be 1 to %d bytes of text", name, maxBytes)
	}
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("the %s contains a character that cannot be shown", name)
		}
	}
	return nil
}

func publicKeyOfCSR(csrPEM string) (crypto.PublicKey, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("the certificate request is not exactly one PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("reading the certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("the certificate request is not signed by its own key: %w", err)
	}
	return csr.PublicKey, nil
}
