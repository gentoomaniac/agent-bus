package broker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"

	"github.com/nats-io/nats.go"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

var errHandshakeSeen = errors.New("certificate chain captured, connection dropped on purpose")

// FetchCA learns the bus's CA certificate from the broker's TLS handshake, for a machine that has
// none yet, and accepts it only if its SHA-256 fingerprint is the one the owner gave (`agent-busctl
// ca fingerprint`) and it signs the broker's certificate for the host. The handshake is aborted
// before the client says anything, so no password goes to a broker that is not yet trusted.
// It needs the broker to send the CA certificate after its own (tls init does that).
func FetchCA(address, fingerprint string) ([]byte, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("broker address %q: %w", address, err)
	}
	var chain [][]byte
	config := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         host,
		InsecureSkipVerify: true, // the chain is checked below, against the owner's fingerprint
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			chain = raw
			return errHandshakeSeen
		},
	}
	_, err = nats.Connect(address, nats.Secure(config), nats.NoReconnect(), nats.Timeout(connectTimeout))
	if len(chain) == 0 {
		return nil, fmt.Errorf("connecting to %s: %w", address, err)
	}

	certs := make([]*x509.Certificate, len(chain))
	for i, der := range chain {
		if certs[i], err = x509.ParseCertificate(der); err != nil {
			return nil, err
		}
	}
	for _, candidate := range certs[1:] {
		if !candidate.IsCA || !tlsinit.SameFingerprint(tlsinit.Fingerprint(candidate.Raw), fingerprint) {
			continue
		}
		roots := x509.NewCertPool()
		roots.AddCert(candidate)
		if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
			return nil, fmt.Errorf("the CA with that fingerprint does not vouch for the broker at %s: %w", address, err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: candidate.Raw}), nil
	}
	return nil, fmt.Errorf("the broker at %s sent no CA certificate with the fingerprint %s: it is not the owner's broker, "+
		"or its broker.pem lacks the CA (docs/podman.md); copy ca.pem from the central machine instead", address, fingerprint)
}
