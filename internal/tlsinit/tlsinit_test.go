package tlsinit

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
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

func TestRenewBrokerKeepsTheCAAndChangesTheNames(t *testing.T) {
	opts := testOptions(t.TempDir())
	if err := Create(opts); err != nil {
		t.Fatal(err)
	}
	caBefore, _ := os.ReadFile(opts.CACertPath)
	keyBefore, _ := os.ReadFile(opts.CAKeyPath)

	opts.SANs = []string{"localhost", "10.1.1.107", "bus.lan"}
	if err := RenewBroker(opts); err != nil {
		t.Fatal(err)
	}
	if caAfter, _ := os.ReadFile(opts.CACertPath); string(caAfter) != string(caBefore) {
		t.Error("the CA certificate changed")
	}
	if keyAfter, _ := os.ReadFile(opts.CAKeyPath); string(keyAfter) != string(keyBefore) {
		t.Error("the CA key changed")
	}
	ca := readCert(t, opts.CACertPath)
	broker := readCert(t, opts.BrokerCertPath)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, name := range []string{"localhost", "10.1.1.107", "bus.lan"} {
		if _, err := broker.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Errorf("not valid for %s: %v", name, err)
		}
	}
	if _, err := broker.Verify(x509.VerifyOptions{Roots: pool, DNSName: "broker.lan"}); err == nil {
		t.Error("still valid for a name that was dropped")
	}
	if _, err := os.Stat(opts.BrokerKeyPath); err != nil {
		t.Error(err)
	}
	// The CA follows the broker certificate, for new machines that check it by fingerprint.
	data, _ := os.ReadFile(opts.BrokerCertPath)
	if rest := data; strings.Count(string(rest), "BEGIN CERTIFICATE") != 2 {
		t.Errorf("broker.pem holds %d certificates, want 2", strings.Count(string(rest), "BEGIN CERTIFICATE"))
	}
}

func TestRenewBrokerNeedsTheCA(t *testing.T) {
	if err := RenewBroker(testOptions(t.TempDir())); err == nil {
		t.Error("renewed without a CA")
	}
}
