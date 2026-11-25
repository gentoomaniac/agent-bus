//go:build integration

package itest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// terminationBudget is how long a harness may expect `agent-bus mcp` to take to stop after a
// signal, before it sends SIGKILL.
const terminationBudget = 2 * time.Second

// An `agent-bus mcp` whose client holds stdin open and says nothing must still stop on SIGTERM
// and SIGINT, with the broker up or down, and give the identity back.
func TestMCPStopsOnSignal(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")
	for _, brokerUp := range []bool{true, false} {
		h := startBus(t)
		env := h.env
		if !brokerUp {
			deadPort := strconv.Itoa(freePort(t))
			env = append(env, "BUS_BROKER=localhost:"+deadPort, "BUS_MAPPED_PORT="+deadPort)
		}
		for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
			t.Run(fmt.Sprintf("%v with the broker up %v", signal, brokerUp), func(t *testing.T) {
				cmd := exec.Command(agentBus, "mcp", "--agent", alice)
				cmd.Env = env
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				exited := make(chan error, 1)
				go func() { exited <- cmd.Wait() }()
				t.Cleanup(func() { cmd.Process.Kill() })
				select { // let it take the identity and block on stdin
				case <-exited:
					t.Fatal("exited before the signal")
				case <-time.After(time.Second):
				}

				if err := cmd.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
				select {
				case <-exited:
				case <-time.After(terminationBudget):
					t.Fatalf("still running %v after %v", terminationBudget, signal)
				}
				lock, err := identity.AcquireLock(creds.AgentDir(h.configDir, alice))
				if err != nil {
					t.Fatalf("the identity is still held: %v", err)
				}
				lock.Release()
			})
		}
	}
}

// A tool call in flight, the way Claude Code leaves it when it quits during wait_for_messages,
// does not hold the signal up either.
func TestMCPStopsOnSignalDuringAToolCall(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")
	h := startBus(t)
	server := h.startMCPServer(t, agentBus, alice)
	if text, isError := server.call(t, "whoami", nil); isError {
		t.Fatal(text)
	}
	go server.client.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_for_messages", Arguments: map[string]any{"timeout_s": 60}})
	time.Sleep(time.Second)

	if err := server.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { server.client.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(terminationBudget):
		t.Fatalf("still running %v after SIGTERM", terminationBudget)
	}
}
