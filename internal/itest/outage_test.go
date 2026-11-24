//go:build integration

package itest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
)

const lateAgent = "late@alice"

// eventually polls until check succeeds, so a test does not depend on how fast a reconnect is.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// goBuild compiles one of the commands into a temporary directory.
func goBuild(t *testing.T, command string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), command)
	if output, err := exec.Command("go", "build", "-o", binary, "../../cmd/"+command).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", command, err, output)
	}
	return binary
}

// startMonitor runs the bus-monitor command on a fresh port and returns its web address as the
// URL of a test server, which the helpers of monitor_test.go read from.
func (h *harness) startMonitor(t *testing.T, busMonitor string) *httptest.Server {
	t.Helper()
	address := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cmd := exec.Command(busMonitor, "--listen", address)
	cmd.Env = h.env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return &httptest.Server{URL: "http://" + address}
}

// busState is what the monitor says about its connection to the bus, or "" while it does not answer.
func busState(server *httptest.Server) string {
	response, err := http.Get(server.URL + "/api/status")
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	var status monitor.Status
	json.NewDecoder(response.Body).Decode(&status)
	return status.Bus
}

type inboxMessage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// inbox returns the texts of the unread messages, with their IDs.
func inbox(t *testing.T, server *mcpServer) (texts, ids []string) {
	t.Helper()
	text, isError := server.call(t, "inbox", nil)
	var out struct{ Messages []inboxMessage }
	if isError || json.Unmarshal([]byte(text), &out) != nil {
		t.Fatalf("inbox: %s", text)
	}
	for _, message := range out.Messages {
		texts, ids = append(texts, message.Text), append(ids, message.ID)
	}
	slices.Sort(texts)
	return texts, ids
}

func send(t *testing.T, server *mcpServer, to, text string) (output string, isError bool) {
	t.Helper()
	return server.call(t, "send", map[string]any{"to": to, "body": map[string]any{"text": text}})
}

func TestBrokerOutage(t *testing.T) {
	agentBus, busMonitor := goBuild(t, "agent-bus"), goBuild(t, "bus-monitor")
	h := startBus(t)
	h.register(t, lateAgent)
	h.env = append(h.env, "BUS_RECONNECT_INITIAL=100ms", "BUS_RECONNECT_MAX=500ms")

	web := h.startMonitor(t, busMonitor)
	aliceServer, bobServer := h.startMCPServer(t, agentBus, alice), h.startMCPServer(t, agentBus, bob)
	eventually(t, "the monitor to be live", func() bool { return busState(web) == monitor.BusLive })

	// Mail that bob's reader has buffered and bob has not acked when the broker goes down.
	if text, isError := send(t, aliceServer, bob, "before the outage"); isError {
		t.Fatalf("send: %s", text)
	}
	eventually(t, "bob to buffer the first message", func() bool {
		texts, _ := inbox(t, bobServer)
		return len(texts) == 1
	})
	// The monitor has read it before the broker goes away, so what follows tests resuming, not a first read.
	monitorChannel := "dm:" + alice + ":" + bob
	waitForMonitorMessages(t, web, monitorChannel, 1)

	h.run(t, natsScript, "down")

	t.Run("tools that need the broker report the outage, the others work from the buffer", func(t *testing.T) {
		_, ids := inbox(t, bobServer)
		for tool, arguments := range map[string]map[string]any{
			"send":      {"to": alice, "body": map[string]any{"text": "x"}},
			"publish":   {"channel": "general", "body": map[string]any{"text": "x"}, "targets": []string{"@all"}},
			"mark_read": {"message_ids": ids},
		} {
			eventually(t, tool+" to report the outage", func() bool {
				text, isError := bobServer.call(t, tool, arguments)
				return isError && strings.Contains(text, "bus unavailable, reconnecting (next attempt in")
			})
		}
		if texts, _ := inbox(t, bobServer); !slices.Equal(texts, []string{"before the outage"}) {
			t.Errorf("inbox during the outage = %v", texts)
		}
		for _, tool := range []string{"whoami", "list_channels"} {
			if text, isError := bobServer.call(t, tool, nil); isError {
				t.Errorf("%s: %s", tool, text)
			}
		}
		// The inbox call above already returned the buffered message, so wait has nothing new.
		if text, isError := bobServer.call(t, "wait_for_messages", map[string]any{"timeout_s": 1}); isError || strings.Contains(text, "before the outage") {
			t.Errorf("wait_for_messages during the outage: error=%v %s", isError, text)
		}
		eventually(t, "the monitor to show the outage", func() bool { return busState(web) == monitor.BusReconnecting })
		if shown := getJSON[[]monitor.Message](t, web, "/api/messages?channel="+monitorChannel); len(shown) != 1 {
			t.Errorf("the monitor shows %d messages during the outage, want the one it had read", len(shown))
		}
	})

	// A session that starts while the broker is down, as after a reboot of the owner's machine.
	lateServer := h.startMCPServer(t, agentBus, lateAgent)
	if text, isError := lateServer.call(t, "whoami", nil); isError {
		t.Fatalf("whoami of a server started without the broker: %s", text)
	}

	h.run(t, natsScript, "up")

	t.Run("everything resumes, and nothing is lost or doubled", func(t *testing.T) {
		eventually(t, "the monitor to be live again", func() bool { return busState(web) == monitor.BusLive })
		eventually(t, "bob to send again", func() bool { _, isError := send(t, bobServer, alice, "bob is back"); return !isError })
		eventually(t, "the late server to send", func() bool { _, isError := send(t, lateServer, bob, "late is up"); return !isError })
		if text, isError := send(t, aliceServer, bob, "after the outage"); isError {
			t.Fatalf("send after the outage: %s", text)
		}

		want := []string{"after the outage", "before the outage", "late is up"}
		eventually(t, "bob to read all three messages", func() bool { texts, _ := inbox(t, bobServer); return len(texts) >= len(want) })
		texts, ids := inbox(t, bobServer)
		if !slices.Equal(texts, want) {
			t.Errorf("bob's inbox = %v, want %v", texts, want)
		}
		if text, isError := bobServer.call(t, "mark_read", map[string]any{"message_ids": ids}); isError {
			t.Errorf("mark_read after the outage: %s", text)
		}
		if texts, _ := inbox(t, bobServer); len(texts) != 0 {
			t.Errorf("acked messages are back: %v", texts)
		}

		// The monitor kept what it had and caught up on the rest, each message once.
		messages := waitForMonitorMessages(t, web, monitorChannel, 3)
		var shown []string
		for _, message := range messages {
			shown = append(shown, message.Text)
		}
		slices.Sort(shown)
		if want := []string{"after the outage", "before the outage", "bob is back"}; !slices.Equal(shown, want) {
			t.Errorf("the monitor shows %v, want %v", shown, want)
		}
	})
}

// A restarting broker may refuse logins until it has loaded its users; the session must keep
// trying instead of giving up after the same authentication error twice.
func TestReconnectSurvivesRejectedLogins(t *testing.T) {
	agentBus := goBuild(t, "agent-bus")
	h := startBus(t)
	h.env = append(h.env, "BUS_RECONNECT_INITIAL=100ms", "BUS_RECONNECT_MAX=300ms")
	aliceServer, bobServer := h.startMCPServer(t, agentBus, alice), h.startMCPServer(t, agentBus, bob)
	if text, isError := send(t, bobServer, alice, "warm up"); isError {
		t.Fatalf("send: %s", text)
	}

	ctx := timeout(t)
	admin := h.admin(t)
	password, err := creds.ReadPassword(creds.AgentDir(h.configDir, bob))
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.DeleteAccount(ctx, bob); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to notice the lost login", func() bool { _, isError := send(t, bobServer, alice, "x"); return isError })
	time.Sleep(2 * time.Second) // many rejected attempts, more than nats.go tolerates by default
	if err := admin.CreateAccount(ctx, bob, password); err != nil {
		t.Fatal(err)
	}

	eventually(t, "bob to send again", func() bool { _, isError := send(t, bobServer, alice, "bob is back"); return !isError })
	if text, isError := send(t, aliceServer, bob, "welcome back"); isError {
		t.Fatalf("send: %s", text)
	}
	eventually(t, "bob to read the message", func() bool { texts, _ := inbox(t, bobServer); return len(texts) >= 1 })
	if texts, _ := inbox(t, bobServer); !slices.Equal(texts, []string{"welcome back"}) {
		t.Errorf("bob's inbox = %v, want the one message", texts)
	}
}
