package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestReadPrivateKeyReturnsWhatWasWritten(t *testing.T) {
	_, private := Generate()
	dir := t.TempDir()
	if err := WritePrivateKey(dir, private); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrivateKey(dir)
	if err != nil || !got.Equal(private) {
		t.Errorf("ReadPrivateKey = %v, %v", got, err)
	}
	if _, err := ReadPrivateKey(t.TempDir()); err == nil {
		t.Error("a missing key was read")
	}
}

func TestLockIsExclusiveAndNamesTheHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLock(dir)
	var held HeldError
	if !errors.As(err, &held) || held.HolderPID != strconv.Itoa(os.Getpid()) {
		t.Fatalf("second lock: %v, want HeldError naming pid %d", err, os.Getpid())
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("the identity is not free after release: %v", err)
	}
	second.Release()
}

// A killed holder leaves its PID in the file, and the file must not keep anyone out.
func TestLockOfADeadHolderDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lockFile), []byte("2147483646\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("a leftover PID blocks the identity: %v", err)
	}
	lock.Release()
}

func TestHeldErrorSaysWhatToDo(t *testing.T) {
	for _, pid := range []string{"4242", ""} {
		message := HeldError{HolderPID: pid}.Error()
		if !strings.Contains(message, pid) || !strings.Contains(message, "stop") || !strings.Contains(message, "restart") {
			t.Errorf("HeldError{%q} = %q, want the holder and what to do", pid, message)
		}
	}
}
