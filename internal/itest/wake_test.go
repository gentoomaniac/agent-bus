//go:build integration

package itest

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startWaiter runs `agent-bus pending --wait` the way the asyncRewake hook does, as a child of this
// process, which is also the parent of bob's MCP server.
func (h *harness) startWaiter(t *testing.T, agentBus string) (exited <-chan error, stderr *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(agentBus, "pending", "--wait", "--max=60s")
	cmd.Env = h.env
	stderr = &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, stderr
}

func TestIdleSessionIsWokenByTargetedMailOnly(t *testing.T) {
	agentBus := filepath.Join(t.TempDir(), "agent-bus")
	build := exec.Command("go", "build", "-o", agentBus, "../../cmd/agent-bus")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build agent-bus: %v\n%s", err, output)
	}
	const carol = "carol@alice"

	h := startBus(t)
	h.register(t, carol)
	h.startMCPServer(t, agentBus, bob)
	// Alice's server sits behind a shell, so it listens under another PID than bob's.
	aliceSession := h.connectMCP(t, exec.Command("sh", "-c", `"$0" "$@"; status=$?; exit $status`, agentBus, "mcp", "--agent", alice))
	waiter, stderr := h.startWaiter(t, agentBus)

	if text, isError := aliceSession.call(t, "publish", map[string]any{"channel": "general", "targets": []string{carol}, "body": map[string]any{"text": "chatter between others"}}); isError {
		t.Fatalf("publish: %s", text)
	}
	select {
	case err := <-waiter:
		t.Fatalf("the waiter ended for chatter: %v, %s", err, stderr)
	case <-time.After(3 * time.Second):
	}

	if text, isError := aliceSession.call(t, "send", map[string]any{"to": bob, "type": "question", "body": map[string]any{"title": "wake up", "text": "are you there?"}}); isError {
		t.Fatalf("send: %s", text)
	}
	select {
	case err := <-waiter:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("the waiter exited with %v, want exit code 2", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("targeted mail did not wake the waiter")
	}
	for _, want := range []string{"1 new message", "`inbox`", "untrusted peer data", alice, "wake up"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the wake notice lacks %q: %s", want, stderr)
		}
	}
	if out := h.runHook(t, agentBus, "hook"); out != "" {
		t.Errorf("the Stop hook delivers the mail that woke the session: %s", out)
	}
}
