package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSetsModeAndCreatesPrivateDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "key")
	if err := Write(path, []byte("secret"), SecretMode, false); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, SecretMode)
	assertMode(t, filepath.Dir(path), DirMode)
}

func TestWriteRefusesToOverwriteUnlessAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := Write(path, []byte("old"), SecretMode, false); err != nil {
		t.Fatal(err)
	}
	if Write(path, []byte("new"), SecretMode, false) == nil {
		t.Fatal("overwrite without the flag succeeded")
	}
	if err := Write(path, []byte("new"), SecretMode, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
}

func TestOverwriteReplacesTheFileWholeAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	if err := Write(path, []byte("old content that is longer"), PublicMode, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), SecretMode, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("content = %q, want exactly new", got)
	}
	assertMode(t, path, SecretMode)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d entries in the directory, want only the file", len(entries))
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func TestAnOverwriteFailsWhenTheDirectoryCannotBeSynced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	var synced []string
	defer func(original func(string) error) { syncDirectory = original }(syncDirectory)
	syncDirectory = func(dir string) error { synced = append(synced, dir); return errors.New("disk gone") }

	if err := Write(path, []byte("x"), PublicMode, true); err == nil || err.Error() != "disk gone" {
		t.Errorf("Write = %v, want the directory's sync error", err)
	}
	if len(synced) != 1 || synced[0] != filepath.Dir(path) {
		t.Errorf("synced %v, want the file's directory", synced)
	}
}
