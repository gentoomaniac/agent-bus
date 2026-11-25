package tlsinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFingerprintOfThePEMMatchesAnyWritingOfIt(t *testing.T) {
	dir := t.TempDir()
	err := Create(Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "b.pem"), BrokerKeyPath: filepath.Join(dir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	fingerprint, err := FingerprintOfPEM(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 32*3-1 || strings.Count(fingerprint, ":") != 31 {
		t.Fatalf("fingerprint %q is not 32 colon-separated bytes", fingerprint)
	}
	for _, written := range []string{fingerprint, strings.ToLower(fingerprint), strings.ReplaceAll(fingerprint, ":", ""), " " + fingerprint + " "} {
		if !SameFingerprint(fingerprint, written) {
			t.Errorf("%q differs from %q", written, fingerprint)
		}
	}
	for name, other := range map[string]string{
		"empty": "", "short": fingerprint[:20], "not hex": strings.Repeat("zz:", 32),
		"one byte off": "00" + fingerprint[2:],
	} {
		if SameFingerprint(fingerprint, other) {
			t.Errorf("%s fingerprint matched", name)
		}
	}
	if _, err := FingerprintOfPEM([]byte("nothing")); err == nil {
		t.Error("a fingerprint of no certificate")
	}
}
