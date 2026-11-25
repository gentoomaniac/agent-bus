package creds

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnEmptyMonitorPasswordFileIsAnErrorNotNoPassword(t *testing.T) {
	for _, content := range []string{"", "  \n\t\n"} {
		dir := t.TempDir()
		path := filepath.Join(dir, monitorPasswordFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if hash, err := ReadMonitorPasswordHash(dir); err == nil {
			t.Errorf("file %q read as hash %q without an error", content, hash)
		}
	}
}

func TestThePasswordIsReadFromTheOldPlaceUntilANewOneIsWritten(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, oldMonitorPasswordFile)
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old-hash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hash, err := ReadMonitorPasswordHash(dir); err != nil || string(hash) != "old-hash" {
		t.Fatalf("hash %q, error %v; want the old file's hash", hash, err)
	}
	if err := WriteMonitorPassword(dir, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if hash, err := ReadMonitorPasswordHash(dir); err != nil || string(hash) == "old-hash" {
		t.Fatalf("hash %q, error %v; want the new file's hash", hash, err)
	}
	if _, err := os.Stat(MonitorPasswordPath(dir)); err != nil {
		t.Errorf("the new file was not written: %v", err)
	}
}
