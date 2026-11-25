// Package enrollclient is `agent-bus enroll`: a machine asks busd for a client certificate for an
// agent ID, the owner approves it, and the machine ends up with the certificate, the CA and the
// directory (docs/design/access-control.md). The private keys are made here and never leave.
package enrollclient

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	pendingFile = "enroll-pending.json"
	// DefaultWait is how long a bare --waitforcert waits: as long as a request lives.
	DefaultWait = 24 * time.Hour
	// defaultExchangeTimeout bounds one question to busd. A question busd never answers (it
	// stopped after receiving it) would otherwise hold the whole wait, though busd may be back and
	// the request approved a moment later; after the timeout the loop asks again.
	defaultExchangeTimeout = 10 * time.Second
)

// Options say who asks for what, and whom to trust.
type Options struct {
	ConfigDir      string
	AgentID        string
	Broker         string // host:port of the password listener, where the enrollment login is
	CACertPath     string // default: ca.pem in ConfigDir
	CAFingerprint  string // from `agent-busctl ca fingerprint`; needed when there is no CA certificate yet
	EnrollPassword string // of the shared enrollment login
	Secret         string // the autosign secret, if the owner gave one
	Wait           time.Duration
	Out            io.Writer
}

// Requester is the connection to busd (broker.Requester).
type Requester interface {
	Request(ctx context.Context, subject string, body []byte) ([]byte, error)
}

// pending is what a machine keeps between asking and being approved: the request (without the
// autosign secret) and the TLS key its certificate request is for.
type pending struct {
	Request enroll.Request `json:"request"`
	TLSKey  string         `json:"tls_key"` // PEM
}

// Run asks for the certificate and, if Wait is set, waits for the answer. Without waiting it
// returns nil with the request pending, and running it again looks at the same request.
func Run(ctx context.Context, opts Options) error {
	if err := bus.ValidateAgentID(opts.AgentID); err != nil {
		return err
	}
	caPath := opts.CACertPath
	if caPath == "" {
		caPath = filepath.Join(opts.ConfigDir, "ca.pem")
	}
	caPEM, err := trustedCA(caPath, opts)
	if err != nil {
		return err
	}
	backoff := broker.DefaultBackoff
	requester, err := newRequester(opts.Broker, caPath, opts.EnrollPassword, &backoff)
	if err != nil {
		return err
	}
	defer requester.Close()
	return (&enrolment{
		opts: opts, requester: requester, caPEM: caPEM, dir: creds.AgentDir(opts.ConfigDir, opts.AgentID),
		pollEvery: defaultPoll, exchangeTimeout: defaultExchangeTimeout,
	}).run(ctx)
}

// newRequester logs in as the shared enrollment user, with an inbox of its own below
// enroll.InboxPrefix.
func newRequester(address, caPath, password string, reconnect *broker.Backoff) (*broker.Requester, error) {
	inbox := make([]byte, 8)
	if _, err := rand.Read(inbox); err != nil {
		return nil, err
	}
	return broker.NewRequester(broker.Connection{
		Address: address, CACertPath: caPath, User: enroll.EnrollUser, Password: password, Reconnect: reconnect,
	}, enroll.InboxPrefix+"."+hex.EncodeToString(inbox))
}

// trustedCA returns the CA certificate this machine trusts. One that was copied here is used (and
// checked against the fingerprint if one is given). With none, the fingerprint is required and
// the CA is fetched from the broker's handshake and kept only if it matches: a CA learned from
// the broker alone is never trusted.
func trustedCA(path string, opts Options) ([]byte, error) {
	caPEM, err := os.ReadFile(path)
	switch {
	case err == nil:
		if opts.CAFingerprint != "" {
			actual, err := tlsinit.FingerprintOfPEM(caPEM)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if !tlsinit.SameFingerprint(actual, opts.CAFingerprint) {
				return nil, fmt.Errorf("%s has the fingerprint %s, not %s: it is not the owner's CA", path, actual, opts.CAFingerprint)
			}
		}
		return caPEM, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	case opts.CAFingerprint == "":
		return nil, fmt.Errorf("no CA certificate at %s: copy ca.pem from the central machine, or pass --ca-fingerprint "+
			"(`agent-busctl ca fingerprint` there) to fetch it from the broker and check it", path)
	}
	caPEM, err = broker.FetchCA(opts.Broker, opts.CAFingerprint)
	if err != nil {
		return nil, err
	}
	if err := files.Write(path, caPEM, files.PublicMode, false); err != nil {
		return nil, err
	}
	fmt.Fprintf(opts.Out, "the CA certificate matches %s and is saved to %s\n", opts.CAFingerprint, path)
	return caPEM, nil
}

type enrolment struct {
	opts      Options
	requester Requester
	caPEM     []byte
	dir       string
	pollEvery broker.Backoff // the status questions of --waitforcert

	exchangeTimeout time.Duration // per question, inside the whole wait
}

var defaultPoll = broker.Backoff{Initial: 2 * time.Second, Max: 30 * time.Second}

func (e *enrolment) run(ctx context.Context) error {
	waiting, err := e.load()
	if err != nil {
		return err
	}
	request := waiting.Request
	isNew := request.AgentID == ""
	if isNew {
		if request, waiting.TLSKey, err = e.newRequest(); err != nil {
			return err
		}
		waiting.Request = request
		if err := e.save(waiting); err != nil {
			return err
		}
		fmt.Fprintf(e.opts.Out, "asking for a certificate for %s\n", e.opts.AgentID)
	} else {
		fmt.Fprintf(e.opts.Out, "the request for %s was made before; asking how it is doing\n", e.opts.AgentID)
	}
	fmt.Fprintf(e.opts.Out, "approval code %s, signing key %s: the owner compares them with what `agent-busctl ca review` shows\n",
		request.Code(), fingerprintOfRequestKey(request))

	if e.opts.Wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.opts.Wait)
		defer cancel()
	}
	for attempt := 1; ; attempt++ {
		reply, err := e.ask(ctx, request, isNew && attempt == 1)
		if err != nil && e.opts.Wait == 0 {
			return err
		}
		if err == nil {
			switch reply.Status {
			case enroll.StatusApproved:
				return e.finish(request, waiting.TLSKey, reply)
			case enroll.StatusRejected:
				_ = os.Remove(e.pendingPath())
				return fmt.Errorf("the request %s was rejected: %s", request.Code(), enroll.Printable(reply.Message))
			case enroll.StatusPending:
				if e.opts.Wait == 0 {
					fmt.Fprintf(e.opts.Out, "waiting for the owner to approve %s; run the command again, or with --waitforcert, to collect the certificate\n", request.Code())
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no decision on %s within %s: run the command again to keep waiting", request.Code(), e.opts.Wait)
		case <-time.After(e.pollEvery.Delay(attempt)):
		}
	}
}

// ask learns the state of the request. It sends the request when asked to submit, or when busd
// does not know it (busd lost its state), and asks for the status otherwise. Only an answer that
// names this request is believed: the enrollment login is shared, so anyone who has it can make
// busd answer on this machine's inbox with something else, a refusal included. Anything else is
// an error to try again.
func (e *enrolment) ask(ctx context.Context, request enroll.Request, submit bool) (enroll.Reply, error) {
	if !submit {
		reply, err := e.exchange(ctx, enroll.SubjectStatus, enroll.StatusQuery{RequestID: request.ID()})
		if err != nil || reply.Status != enroll.StatusUnknown {
			return believed(request, reply, err)
		}
	}
	request.Secret = e.opts.Secret
	reply, err := e.exchange(ctx, enroll.SubjectRequest, request)
	return believed(request, reply, err)
}

func believed(request enroll.Request, reply enroll.Reply, err error) (enroll.Reply, error) {
	if err == nil && reply.RequestID != request.ID() {
		err = fmt.Errorf("busd answered %q, which is no answer about this request: %s", enroll.Printable(string(reply.Status)), enroll.Printable(reply.Message))
	}
	return reply, err
}

func (e *enrolment) exchange(ctx context.Context, subject string, body any) (enroll.Reply, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return enroll.Reply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.exchangeTimeout)
	defer cancel()
	answer, err := e.requester.Request(ctx, subject, data)
	if err != nil {
		return enroll.Reply{}, err
	}
	var reply enroll.Reply
	if err := json.Unmarshal(answer, &reply); err != nil {
		return enroll.Reply{}, fmt.Errorf("busd's answer is not an enrollment reply: %w", err)
	}
	return reply, nil
}

func (e *enrolment) pendingPath() string { return filepath.Join(e.dir, pendingFile) }

func (e *enrolment) load() (pending, error) {
	data, err := os.ReadFile(e.pendingPath())
	if errors.Is(err, fs.ErrNotExist) {
		return pending{}, nil
	}
	var waiting pending
	if err == nil {
		err = json.Unmarshal(data, &waiting)
	}
	if err != nil {
		return pending{}, fmt.Errorf("reading %s (delete it to start a new request): %w", e.pendingPath(), err)
	}
	return waiting, nil
}

func (e *enrolment) save(waiting pending) error {
	data, err := json.Marshal(waiting)
	if err != nil {
		return err
	}
	return files.Write(e.pendingPath(), data, files.SecretMode, true)
}

// newRequest makes the request: the agent's signing key (an existing identity keeps its key, so
// its pins, consumers and history stay valid) and a new TLS key with its certificate request.
func (e *enrolment) newRequest() (enroll.Request, string, error) {
	signingKey, err := identity.ReadPrivateKey(e.dir)
	if errors.Is(err, fs.ErrNotExist) {
		_, signingKey = identity.Generate()
		err = identity.WritePrivateKey(e.dir, signingKey)
	}
	if err != nil {
		return enroll.Request{}, "", err
	}
	tlsKeyPEM, csrPEM, err := tlsinit.NewClientKeyAndCSR()
	if err != nil {
		return enroll.Request{}, "", err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown"
	}
	request, err := enroll.NewRequest(e.opts.AgentID, signingKey, csrPEM, hostname, "")
	return request, string(tlsKeyPEM), err
}

// finish checks what busd sent and only then installs it: the certificate must be for this agent
// and this machine's TLS key, issued by the CA this machine trusts, and the CA busd sent must be
// that CA. A reply that fails a check is an error, however it got here.
func (e *enrolment) finish(request enroll.Request, tlsKeyPEM string, reply enroll.Reply) error {
	if reply.CA != "" && !sameCertificate(reply.CA, e.caPEM) {
		return errors.New("busd sent a CA certificate other than the one this machine trusts")
	}
	if err := checkCertificate(reply.Certificate, tlsKeyPEM, e.caPEM, e.opts.AgentID); err != nil {
		return fmt.Errorf("the certificate busd sent is not usable: %w", err)
	}
	if err := creds.WriteClientCert(e.dir, []byte(reply.Certificate), []byte(tlsKeyPEM)); err != nil {
		return err
	}
	records := 0
	if reply.Directory != nil {
		records = len(reply.Directory.Records)
		if err := writeDirectory(trust.DirectoryPath(e.opts.ConfigDir), *reply.Directory); err != nil {
			return err
		}
	}
	// Kept so that the running agent can ask busd for the directory later (FetchDirectory).
	if err := creds.WritePassword(filepath.Join(e.opts.ConfigDir, passwordDir), e.opts.EnrollPassword, true); err != nil {
		return err
	}
	_ = os.Remove(e.pendingPath())
	certPath, keyPath := creds.ClientCertPaths(e.dir)
	fmt.Fprintf(e.opts.Out, "enrolled %s: certificate %s, key %s, directory of %d agents\n", e.opts.AgentID, certPath, keyPath, records)
	return nil
}

func writeDirectory(dir trust.FileDirectory, directory enroll.Directory) error {
	for _, record := range directory.Records {
		if err := dir.Write(record); err != nil {
			return err
		}
	}
	return dir.WriteCatalogue(directory.Channels)
}

func fingerprintOfRequestKey(request enroll.Request) string {
	raw, _ := base64.StdEncoding.DecodeString(request.PublicKey)
	return identity.Fingerprint(raw)
}

// sameCertificate says whether the PEM texts hold the same first certificate.
func sameCertificate(a string, b []byte) bool {
	first, _ := pem.Decode([]byte(a))
	second, _ := pem.Decode(b)
	return first != nil && second != nil && bytes.Equal(first.Bytes, second.Bytes)
}

// checkCertificate verifies a certificate against this machine's own expectations.
func checkCertificate(certPEM, tlsKeyPEM string, caPEM []byte, agentID string) error {
	block, rest := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) > 0 {
		return errors.New("not exactly one PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	keyBlock, _ := pem.Decode([]byte(tlsKeyPEM))
	if keyBlock == nil {
		return errors.New("this machine's TLS key is unreadable")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	if !key.Public().(interface{ Equal(crypto.PublicKey) bool }).Equal(cert.PublicKey) {
		return errors.New("it is for another key than this machine's")
	}
	if want := broker.AgentCertIdentity(agentID); len(cert.URIs) != 1 || !creds.MatchesIdentity(cert.URIs[0].String(), want) ||
		len(cert.DNSNames) != 0 || len(cert.EmailAddresses) != 0 || len(cert.IPAddresses) != 0 {
		return fmt.Errorf("it is not for exactly %s", want)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("the trusted CA certificate is unreadable")
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}
