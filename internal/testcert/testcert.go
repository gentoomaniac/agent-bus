// Package testcert makes client certificate pairs for tests.
package testcert

import (
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// Pair returns a certificate and key PEM for identity, signed by a throwaway CA, valid for validity.
func Pair(t *testing.T, identity string, validity time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	dir := t.TempDir()
	options := tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "broker.pem"), BrokerKeyPath: filepath.Join(dir, "broker.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	}
	if err := tlsinit.Create(options); err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(options.CACertPath, options.CAKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, public, err := tlsinit.NewClientKey()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err = ca.IssueClientCert(identity, public, validity)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM
}
