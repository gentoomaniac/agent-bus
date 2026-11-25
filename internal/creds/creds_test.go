package creds

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/testcert"
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

func TestNewPasswordNeverLooksLikeAFlag(t *testing.T) {
	for range 2000 { // one in 64 would start with '-' without the check
		if NewPassword()[0] == '-' {
			t.Fatal("a password starts with '-'")
		}
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
	const identity = "agentbus:admin/bus-admin"
	dir := t.TempDir()
	certPEM, keyPEM := testcert.Pair(t, identity, time.Hour)
	if err := StageClientCert(dir, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if staged, _ := HasStagedClientCert(dir); !staged {
		t.Fatal("the staged pair is not found")
	}
	if has, err := HasClientCert(dir); has || err != nil {
		t.Fatalf("staged pair counts as login: %v, %v", has, err)
	}
	if err := ActivateStagedClientCert(dir, identity); err != nil {
		t.Fatal(err)
	}
	if has, err := HasClientCert(dir); !has || err != nil {
		t.Fatalf("activated: has %v, err %v", has, err)
	}
	if staged, _ := HasStagedClientCert(dir); staged {
		t.Error("the staged files are still there")
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

func TestBrokenStagedPairIsNeverPromoted(t *testing.T) {
	const identity = "agentbus:agent/lib-x_bob"
	dir := t.TempDir()
	working, workingKey := testcert.Pair(t, identity, time.Hour)
	if err := WriteClientCert(dir, working, workingKey); err != nil {
		t.Fatal(err)
	}
	expired, expiredKey := testcert.Pair(t, identity, time.Millisecond)
	other, otherKey := testcert.Pair(t, "agentbus:agent/lib-x_carol", time.Hour)
	_, strangerKey := testcert.Pair(t, identity, time.Hour)
	time.Sleep(10 * time.Millisecond)
	for name, pair := range map[string][2][]byte{
		"expired":        {expired, expiredKey},
		"another agent":  {other, otherKey},
		"mismatched key": {working, strangerKey},
	} {
		if err := StageClientCert(dir, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
		if err := ActivateStagedClientCert(dir, identity); err == nil {
			t.Errorf("%s pair was activated", name)
		}
		certPath, _ := ClientCertPaths(dir)
		if got, _ := os.ReadFile(certPath); string(got) != string(working) {
			t.Errorf("%s pair replaced the working one", name)
		}
	}
}

func TestHasClientCertReportsFilesThatCannotBeExamined(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if has, err := HasClientCert(notADir); has || err == nil {
		t.Errorf("HasClientCert = %v, %v, want the stat error", has, err)
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
