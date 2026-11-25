package tlsinit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newCA(t *testing.T) (*CA, []byte) {
	t.Helper()
	dir := t.TempDir()
	err := Create(Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "b.pem"), BrokerKeyPath: filepath.Join(dir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return ca, caPEM
}

func TestASignatureOfTheCAChecksAgainstItsCertificateOnly(t *testing.T) {
	ca, caPEM := newCA(t)
	_, otherCAPEM := newCA(t)
	signature, err := ca.SignData([]byte("data"))
	if err != nil {
		t.Fatal(err)
	}

	if err := CheckDataSignature(caPEM, []byte("data"), signature); err != nil {
		t.Errorf("the CA's own signature: %v", err)
	}
	if CheckDataSignature(caPEM, []byte("other data"), signature) == nil {
		t.Error("a signature checked for other data")
	}
	if CheckDataSignature(otherCAPEM, []byte("data"), signature) == nil {
		t.Error("a signature checked against another CA")
	}
}
