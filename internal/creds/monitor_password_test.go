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
