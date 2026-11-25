package creds

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	if has, err := HasClientCert(dir); has || err != nil {
		t.Fatalf("an empty directory has a certificate: %v, %v", has, err)
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
	if has, err := HasClientCert(dir); !has || err != nil {
		t.Errorf("the certificate is not found: %v, %v", has, err)
	}
	if err := RemoveClientCert(dir); err != nil {
		t.Fatal(err)
	}
	if has, _ := HasClientCert(dir); has {
		t.Error("the certificate is still there after removal")
	}
	if err := RemoveClientCert(dir); err != nil {
		t.Errorf("removing twice: %v", err)
	}
}

func TestHalfAClientCertPairIsAnError(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := ClientCertPaths(dir)
	for path, want := range map[string]string{certPath: "client.pem without client.key", keyPath: "client.key without client.pem"} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := HasClientCert(dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("HasClientCert = %v, want %q", err, want)
		}
		os.Remove(path)
	}
}

func TestStagedClientCertDoesNotCountUntilActivated(t *testing.T) {
	dir := t.TempDir()
	if err := StageClientCert(dir, []byte("cert"), []byte("key")); err != nil {
		t.Fatal(err)
	}
	if has, err := HasClientCert(dir); has || err != nil || !HasStagedClientCert(dir) {
		t.Fatalf("staged: has %v, err %v, staged %v", has, err, HasStagedClientCert(dir))
	}
	if err := ActivateStagedClientCert(dir); err != nil {
		t.Fatal(err)
	}
	if has, err := HasClientCert(dir); !has || err != nil || HasStagedClientCert(dir) {
		t.Fatalf("activated: has %v, err %v, staged %v", has, err, HasStagedClientCert(dir))
	}
	if err := RetireClientCert(dir); err != nil {
		t.Fatal(err)
	}
	if has, err := HasClientCert(dir); has || err != nil {
		t.Errorf("retired: has %v, err %v", has, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "client.key.retired")); err != nil {
		t.Error(err)
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
