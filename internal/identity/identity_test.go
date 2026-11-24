package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprintHasSSHStyle(t *testing.T) {
	public, _ := Generate()
	fingerprint := Fingerprint(public)
	// SHA-256 is 32 bytes, which is 43 characters of unpadded base64.
	if !strings.HasPrefix(fingerprint, "SHA256:") || len(fingerprint) != len("SHA256:")+43 {
		t.Errorf("fingerprint = %q", fingerprint)
	}
	other, _ := Generate()
	if Fingerprint(other) == fingerprint {
		t.Error("two keys have the same fingerprint")
	}
}

func TestWritePrivateKeyRoundTripsWithPrivateMode(t *testing.T) {
	public, private := Generate()
	dir := filepath.Join(t.TempDir(), "agent")
	if err := WritePrivateKey(dir, private); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(dir)
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %o, want 700", dirInfo.Mode().Perm())
	}

	data, _ := os.ReadFile(path)
	block, _ := pem.Decode(data)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.(ed25519.PrivateKey).Public().(ed25519.PublicKey).Equal(public) {
		t.Error("stored key does not match the generated public key")
	}
	if WritePrivateKey(dir, private) == nil {
		t.Error("an existing key was overwritten")
	}
}
