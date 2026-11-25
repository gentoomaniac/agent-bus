//go:build integration

package itest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killBroker ends nats-server the hard way, as a crash or a lost host does, with no goodbye to its clients.
func (h *harness) killBroker(t *testing.T) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(h.configDir, "nats", "nats-server.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventually(t, "nats-server to be gone", func() bool { return syscall.Kill(pid, 0) != nil })
}

// A broker that crashes and comes back is no reason to call the session's /mcp: the adapter reads
// again, and the waiter wakes the session for mail sent after the restart.
func TestHooksResumeAfterTheBrokerWasKilled(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")
	h := startBus(t)
	h.env = append(h.env, "BUS_RECONNECT_INITIAL=100ms", "BUS_RECONNECT_MAX=500ms")
	h.startMCPServer(t, agentBus, bob)
	aliceSession := h.connectMCP(t, exec.Command(notAWrapper(t, "sh"), "-c", `"$0" "$@"; status=$?; exit $status`, agentBus, "mcp", "--agent", alice))
	waiter, stderr := h.startWaiter(t, agentBus)

	h.killBroker(t)
	time.Sleep(time.Second)
	h.run(t, natsScript, "up")

	eventually(t, "alice to send again", func() bool {
		_, isError := send(t, aliceSession, bob, "after the crash")
		return !isError
	})
	select {
	case err := <-waiter:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("the waiter exited with %v, want exit code 2: %s", err, stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("mail sent after the broker came back did not wake the waiter")
	}
	if !strings.Contains(stderr.String(), "after the crash") {
		t.Errorf("the wake notice lacks the mail: %s", stderr)
	}
}

// A session whose MCP server died is told so once, by the Stop hook or by the waiter, whichever
// asks first; nothing is told about a session that never had one.
func TestHooksTellOnceThatTheMCPServerIsGone(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")

	t.Run("stop hook", func(t *testing.T) {
		h := startBus(t)
		server := h.startMCPServer(t, agentBus, bob)
		if out := h.runHook(t, agentBus, "hook"); out != "" {
			t.Fatalf("the hook of a live server printed %s", out)
		}
		server.cmd.Process.Kill()
		server.cmd.Wait()

		if out := h.runHook(t, agentBus, "hook"); !strings.Contains(out, "/mcp") || !strings.Contains(out, `"decision":"block"`) {
			t.Errorf("the hook did not tell that the server is gone: %q", out)
		}
		if out := h.runHook(t, agentBus, "hook"); out != "" {
			t.Errorf("the loss was told twice: %s", out)
		}
	})

	t.Run("waiter", func(t *testing.T) {
		h := startBus(t)
		server := h.startMCPServer(t, agentBus, bob)
		waiter, stderr := h.startWaiter(t, agentBus)
		time.Sleep(time.Second) // let it reach the adapter
		server.cmd.Process.Kill()

		select {
		case err := <-waiter:
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("the waiter exited with %v, want exit code 2", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the waiter did not notice that the server is gone")
		}
		if !strings.Contains(stderr.String(), "/mcp") {
			t.Errorf("the notice does not name /mcp: %s", stderr)
		}
		if out := h.runHook(t, agentBus, "hook"); out != "" {
			t.Errorf("the Stop hook told what the waiter told: %s", out)
		}
	})

	t.Run("no server ever", func(t *testing.T) {
		h := startBus(t)
		if out := h.runHook(t, agentBus, "hook"); out != "" {
			t.Errorf("a session without a server was told %s", out)
		}
	})
}
