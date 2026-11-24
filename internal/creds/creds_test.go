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
