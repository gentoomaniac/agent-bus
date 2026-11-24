package creds

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
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

func TestPasswordRoundTripWithPrivateMode(t *testing.T) {
	dir := AdminDir(t.TempDir())
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
