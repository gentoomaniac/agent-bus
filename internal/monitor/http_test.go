package monitor

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func getJSON(t *testing.T, server *httptest.Server, path string, into any) *http.Response {
	t.Helper()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

func TestAPI(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "hello"))
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 1)
	server := httptest.NewServer(monitor.Handler())
	defer server.Close()

	var channels []Channel
	getJSON(t, server, "/api/channels", &channels)
	if len(channels) != 1 || channels[0].ID != "general" || channels[0].Messages != 1 {
		t.Errorf("channels = %+v", channels)
	}
	var agents []Agent
	getJSON(t, server, "/api/agents", &agents)
	if len(agents) != 1 || agents[0].ID != alice.id {
		t.Errorf("agents = %+v", agents)
	}
	var members []string
	getJSON(t, server, "/api/members?channel=general", &members)
	if len(members) != 1 || members[0] != alice.id {
		t.Errorf("members = %v", members)
	}
	var messages []Message
	getJSON(t, server, "/api/messages?channel=general", &messages)
	if len(messages) != 1 || messages[0].Status != Verified || messages[0].Text != "hello" {
		t.Errorf("messages = %+v", messages)
	}
	messages = nil
	getJSON(t, server, "/api/messages?channel=general&after=1", &messages)
	if len(messages) != 0 {
		t.Errorf("messages after the last = %+v", messages)
	}
	var status Status
	getJSON(t, server, "/api/status", &status)
	if status.CanSend || status.Note == "" {
		t.Errorf("status = %+v, want read-only with a note", status)
	}

	if code := getJSON(t, server, "/api/members?channel=group:nope", &members).StatusCode; code != http.StatusNotFound {
		t.Errorf("members of an unknown channel: status %d, want 404", code)
	}
	if code := getJSON(t, server, "/api/messages?channel=general&after=x", &messages).StatusCode; code != http.StatusBadRequest {
		t.Errorf("messages with a bad cursor: status %d, want 400", code)
	}
}

func TestServesThePage(t *testing.T) {
	server := httptest.NewServer(runMonitor(t, newFakeBus(t)).Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET /: status %d, content type %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if other, _ := http.Get(server.URL + "/elsewhere"); other.StatusCode != http.StatusNotFound {
		t.Errorf("GET /elsewhere: status %d, want 404", other.StatusCode)
	}
}

func TestEventStreamDeliversNewMessages(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	monitor := runMonitor(t, fake)
	server := httptest.NewServer(monitor.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type %q", response.Header.Get("Content-Type"))
	}
	lines := bufio.NewScanner(response.Body)
	lines.Scan() // ": connected": the subscription exists now

	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "live"))
	found := make(chan Message, 1)
	go func() {
		for lines.Scan() {
			if data, isData := strings.CutPrefix(lines.Text(), "data: "); isData {
				var message Message
				if json.Unmarshal([]byte(data), &message) == nil {
					found <- message
					return
				}
			}
		}
	}()
	select {
	case message := <-found:
		if message.Text != "live" || message.Status != Verified {
			t.Errorf("event = %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
}
