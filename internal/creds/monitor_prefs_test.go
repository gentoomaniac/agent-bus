package creds

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThePrefsLiveNextToThePasswordHash(t *testing.T) {
	dir := t.TempDir()
	if got, want := MonitorPrefsPath(dir), filepath.Join(dir, "monitor-web", "prefs.json"); got != want {
		t.Errorf("without a password: %s, want %s", got, want)
	}
	oldPath := filepath.Join(dir, oldMonitorPasswordFile)
	os.MkdirAll(filepath.Dir(oldPath), 0o700)
	os.WriteFile(oldPath, []byte("old-hash\n"), 0o600)
	if got, want := MonitorPrefsPath(dir), filepath.Join(dir, "monitor", "prefs.json"); got != want {
		t.Errorf("with the old hash: %s, want %s", got, want)
	}
	if err := WriteMonitorPassword(dir, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if got, want := MonitorPrefsPath(dir), filepath.Join(dir, "monitor-web", "prefs.json"); got != want {
		t.Errorf("with the new hash: %s, want %s", got, want)
	}
}
