package tlsinit

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testOptions(dir string) Options {
	return Options{
		CACertPath:     filepath.Join(dir, "ca.pem"),
		CAKeyPath:      filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "broker.pem"),
		BrokerKeyPath:  filepath.Join(dir, "broker.key"),
		SANs:           []string{"localhost", "127.0.0.1", "broker.lan"},
		BrokerValidity: time.Hour,
	}
}

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestCreateIssuesBrokerCertificateTrustedByCA(t *testing.T) {
	opts := testOptions(t.TempDir())
	if err := Create(opts); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(readCert(t, opts.CACertPath))
	broker := readCert(t, opts.BrokerCertPath)
	for _, name := range []string{"localhost", "127.0.0.1", "broker.lan"} {
		if _, err := broker.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("certificate not valid for %s: %v", name, err)
		}
	}
	if _, err := broker.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.lan"}); err == nil {
		t.Error("certificate is valid for a name outside the SANs")
	}
}

func TestCreateSetsFilePermissions(t *testing.T) {
	opts := testOptions(t.TempDir())
	if err := Create(opts); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		opts.CAKeyPath:      0o600,
		opts.BrokerKeyPath:  0o600,
		opts.CACertPath:     0o644,
		opts.BrokerCertPath: 0o644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestCreateRefusesToOverwriteUnlessAsked(t *testing.T) {
	opts := testOptions(t.TempDir())
	if err := Create(opts); err != nil {
		t.Fatal(err)
	}
	if Create(opts) == nil {
		t.Fatal("second Create succeeded without Overwrite")
	}
	opts.Overwrite = true
	if err := Create(opts); err != nil {
		t.Fatalf("Create with Overwrite: %v", err)
	}
}

func TestDefaultSANsCoverLocalhost(t *testing.T) {
	sans := DefaultSANs()
	if sans[0] != "localhost" || sans[1] != "127.0.0.1" {
		t.Errorf("DefaultSANs() = %v, want localhost and 127.0.0.1 first", sans)
	}
}
