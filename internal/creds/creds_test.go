package creds

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNewPasswordIsRandomAndShellSafe(t *testing.T) {
	first, second := NewPassword(), NewPassword()
	if first == second {
		t.Error("two passwords are equal")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(first) {
		t.Errorf("password %q has an unexpected shape", first)
	}
}

func TestClientCertFilesAreWrittenReplacedAndRemoved(t *testing.T) {
	dir := t.TempDir()
	if HasClientCert(dir) {
		t.Fatal("an empty directory has a certificate")
	}
	if err := WriteClientCert(dir, []byte("cert1"), []byte("key1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteClientCert(dir, []byte("cert2"), []byte("key2")); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	certPath, keyPath := ClientCertPaths(dir)
	if got, _ := os.ReadFile(certPath); string(got) != "cert2" {
		t.Errorf("certificate = %q", got)
	}
	if info, _ := os.Stat(keyPath); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", info.Mode().Perm())
	}
	if !HasClientCert(dir) {
		t.Error("the certificate is not found")
	}
	if err := RemoveClientCert(dir); err != nil || HasClientCert(dir) {
		t.Errorf("after removal: %v, has %v", err, HasClientCert(dir))
	}
	if err := RemoveClientCert(dir); err != nil {
		t.Errorf("removing twice: %v", err)
	}
}

func TestMonitorPasswordIsStoredAsAHash(t *testing.T) {
	dir := t.TempDir()
	if hash, err := ReadMonitorPasswordHash(dir); hash != nil || err != nil {
		t.Fatalf("before any password: %q, %v", hash, err)
	}
	if err := WriteMonitorPassword(dir, "short"); err == nil {
		t.Error("a short password was accepted")
	}
	if err := WriteMonitorPassword(dir, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := WriteMonitorPassword(dir, "another password"); err != nil {
		t.Fatalf("replacing the password: %v", err)
	}
	hash, err := ReadMonitorPasswordHash(dir)
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte("another password")) != nil {
		t.Errorf("hash %q does not match the new password (%v)", hash, err)
	}
	info, _ := os.Stat(filepath.Join(dir, monitorPasswordFile))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
}

func TestPasswordRoundTripWithPrivateMode(t *testing.T) {
	dir := AdminDir(t.TempDir(), "bus-admin")
	if err := WritePassword(dir, "s3cret", false); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPassword(dir)
	if err != nil || got != "s3cret" {
		t.Fatalf("ReadPassword = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "password"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("password mode = %o, want 600", info.Mode().Perm())
	}
	if WritePassword(dir, "other", false) == nil {
		t.Error("password was overwritten without the flag")
	}
}
