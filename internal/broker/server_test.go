package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// newFakeServer returns a Server whose nats-server binary is a script that accepts or refuses a
// configuration, whose pid is this test process (a reload is a SIGHUP it catches) and whose
// monitoring endpoint reports a config load time that moves on each SIGHUP when reloads work.
func newFakeServer(t *testing.T, configIsValid, reloadTakes bool) Server {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "nats-server")
	exitCode := map[bool]string{true: "0", false: "1"}[configIsValid]
	script := "#!/bin/sh\necho \"config says no\" >&2\nexit " + exitCode + "\n"
	for name, content := range map[string]string{
		"nats-server": script, serverConfFile: "listen: 1\n", controlAuthFile: "users = []\n",
		pidFile: strconv.Itoa(os.Getpid()), authFile: "# generation 3\nusers = [\n]\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	loadTime := time.Now().UTC()
	hangups := make(chan os.Signal, 4)
	signal.Notify(hangups, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(hangups) })
	go func() {
		for range hangups {
			if reloadTakes {
				mu.Lock()
				loadTime = loadTime.Add(time.Second)
				mu.Unlock()
			}
		}
	}()
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"config_load_time": loadTime})
	}))
	t.Cleanup(monitor.Close)
	return Server{Dir: dir, Binary: binary, MonitorURL: monitor.URL}
}

func readAuth(t *testing.T, server Server) string {
	t.Helper()
	content, err := os.ReadFile(server.path(authFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestUpdateAuthReplacesTheFileAndConfirmsTheReload(t *testing.T) {
	fake := newFakeServer(t, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := fake.UpdateAuth(ctx, func(current string) (string, error) {
		if current != "users = [\n]\n" {
			t.Errorf("edit got %q, want the body without the generation header", current)
		}
		return "users = [\n  new\n]\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readAuth(t, fake), "# generation 4\nusers = [\n  new\n]\n"; got != want {
		t.Errorf("auth.conf = %q, want %q", got, want)
	}
	info, err := os.Stat(fake.path(authFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("auth.conf mode = %v (%v), want 600", info.Mode().Perm(), err)
	}
}

func TestUpdateAuthRefusesAnInvalidCandidateBeforeReplacingTheFile(t *testing.T) {
	fake := newFakeServer(t, false, true)
	before := readAuth(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := fake.UpdateAuth(ctx, func(string) (string, error) { return "garbage", nil })
	if err == nil || !strings.Contains(err.Error(), "config says no") {
		t.Fatalf("UpdateAuth = %v, want the config test's complaint", err)
	}
	if readAuth(t, fake) != before {
		t.Error("an invalid candidate replaced auth.conf")
	}
	if leftovers, _ := filepath.Glob(fake.path("config-test-*")); len(leftovers) != 0 {
		t.Errorf("the config test left %v behind", leftovers)
	}
}

func TestUpdateAuthReportsAReloadThatDidNotTake(t *testing.T) {
	fake := newFakeServer(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := fake.UpdateAuth(ctx, func(string) (string, error) { return "users = []\n", nil })
	if err == nil || !strings.Contains(err.Error(), "did not confirm the reload") {
		t.Fatalf("UpdateAuth = %v, want an unconfirmed reload", err)
	}
}
