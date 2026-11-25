//go:build integration

package itest

import (
	"os/exec"
	"testing"
	"time"
)

// A session that ends cleanly hands its buffered mail back at once; one that is killed cannot, so
// the broker redelivers it only after the consumers' AckWait (docs/troubleshooting.md).
func TestRestartedSessionGetsTheMailTheLastOneBuffered(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")
	for name, tc := range map[string]struct {
		end      func(*mcpServer)
		minDelay time.Duration
		maxDelay time.Duration
	}{
		"closed": {end: func(s *mcpServer) { s.client.Close() }, maxDelay: 10 * time.Second},
		"killed": {end: func(s *mcpServer) { s.cmd.Process.Kill() }, minDelay: 50 * time.Second, maxDelay: 90 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := startBus(t)
			first := h.startMCPServer(t, agentBus, bob)
			// Alice's server sits behind a process that is no wrapper, so the hooks reach bob's only.
			aliceSession := h.connectMCP(t, exec.Command(notAWrapper(t, "sh"), "-c", `"$0" "$@"; exit $?`, agentBus, "mcp", "--agent", alice))
			if text, isError := send(t, aliceSession, bob, "buffered"); isError {
				t.Fatalf("send: %s", text)
			}
			h.waitForMail(t, agentBus)

			tc.end(first)
			first.cmd.Wait()
			start := time.Now()
			second := h.startMCPServer(t, agentBus, bob)
			for texts, _ := inbox(t, second); len(texts) == 0; texts, _ = inbox(t, second) {
				if time.Since(start) > tc.maxDelay {
					t.Fatalf("the mail did not come back within %s", tc.maxDelay)
				}
				time.Sleep(500 * time.Millisecond)
			}
			if delay := time.Since(start); delay < tc.minDelay {
				t.Errorf("the mail came back after %s, before the ack wait of a minute could have run out", delay)
			}
		})
	}
}
