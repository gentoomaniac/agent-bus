package files

import (
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
