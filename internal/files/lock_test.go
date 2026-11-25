package files

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockExcludesAnotherHolderUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.lock") // the directory is made
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if _, err := Lock(path, 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "another process holds") {
		t.Fatalf("a second holder got the lock: %v", err)
	}
	if waited := time.Since(started); waited < 100*time.Millisecond {
		t.Errorf("gave up after %s, before the wait was over", waited)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		unlock()
		close(released)
	}()
	again, err := Lock(path, 5*time.Second) // waits for the release, then gets it
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	default:
		t.Error("the lock was given while the first holder had it")
	}
	again()
}
