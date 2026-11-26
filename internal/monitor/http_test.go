package monitor

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// serve starts the monitor's handler on a test server, as it runs on loopback.
func serve(monitor *Monitor) *httptest.Server {
	return serveWith(monitor, Access{})
}

// serveWith starts the monitor's handler on a test server with the given access, listening where
// the access says unless it names another address.
func serveWith(monitor *Monitor, access Access) *httptest.Server {
	server := httptest.NewUnstartedServer(nil)
	if access.Listen == "" {
		access.Listen = server.Listener.Addr().String()
	}
	handler, err := monitor.Handler(access)
	if err != nil {
		panic(err)
	}
	server.Config.Handler = handler
	server.Start()
	return server
}

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
	server := serve(monitor)
	defer server.Close()

	var channels []Channel
	getJSON(t, server, "/api/v1/channels", &channels)
	if len(channels) != 1 || channels[0].ID != "general" || channels[0].Messages != 1 {
		t.Errorf("channels = %+v", channels)
	}
	var agents []Agent
	getJSON(t, server, "/api/v1/agents", &agents)
	if len(agents) != 1 || agents[0].ID != alice.id {
		t.Errorf("agents = %+v", agents)
	}
	var members []string
	getJSON(t, server, "/api/v1/members?channel=general", &members)
	if len(members) != 1 || members[0] != alice.id {
		t.Errorf("members = %v", members)
	}
	var messages []Message
	getJSON(t, server, "/api/v1/messages?channel=general", &messages)
	if len(messages) != 1 || messages[0].Status != Verified || messages[0].Text != "hello" {
		t.Errorf("messages = %+v", messages)
	}
	messages = nil
	getJSON(t, server, "/api/v1/messages?channel=general&after=1", &messages)
	if len(messages) != 0 {
		t.Errorf("messages after the last = %+v", messages)
	}
	var status Status
	getJSON(t, server, "/api/v1/status", &status)
	if status.CanSend || status.Note == "" {
		t.Errorf("status = %+v, want read-only with a note", status)
	}

	if code := getJSON(t, server, "/api/v1/members?channel=group:nope", &members).StatusCode; code != http.StatusNotFound {
		t.Errorf("members of an unknown channel: status %d, want 404", code)
	}
	if code := getJSON(t, server, "/api/v1/messages?channel=general&after=x", &messages).StatusCode; code != http.StatusBadRequest {
		t.Errorf("messages with a bad cursor: status %d, want 400", code)
	}
}

func TestErrorsAreJSONWithACode(t *testing.T) {
	server := serve(runMonitor(t, newFakeBus(t)))
	defer server.Close()
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/api/v1/members?channel=group:nope": {http.StatusNotFound, "unknown_channel"},
		"/api/v1/messages?after=x":           {http.StatusBadRequest, "bad_request"},
		"/api/status":                        {http.StatusNotFound, "not_found"}, // the unversioned paths are gone
	} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error, Code string }
		json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if response.StatusCode != want.status || body.Code != want.code || body.Error == "" {
			t.Errorf("GET %s: status %d, body %+v, want %d %s with a message", path, response.StatusCode, body, want.status, want.code)
		}
	}
}

func TestServesThePage(t *testing.T) {
	server := serve(runMonitor(t, newFakeBus(t)))
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
	server := serve(monitor)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/events")
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
	eventID := make(chan string, 1)
	go func() {
		var id string
		for lines.Scan() {
			if value, isID := strings.CutPrefix(lines.Text(), "id: "); isID {
				id = value
			}
			if data, isData := strings.CutPrefix(lines.Text(), "data: "); isData {
				var message Message
				if json.Unmarshal([]byte(data), &message) == nil {
					eventID <- id
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
		if id := <-eventID; id != "general#1" {
			t.Errorf("event id = %q, want general#1", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
	}
}

func TestHandlerRefusesAForeignHostHeader(t *testing.T) {
	server := serve(runMonitor(t, newFakeBus(t)))
	defer server.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")

	for host, want := range map[string]int{
		server.Listener.Addr().String(): http.StatusOK,
		"localhost:" + port:             http.StatusOK,
		"LOCALHOST:" + port:             http.StatusOK,
		"127.0.0.1:" + port:             http.StatusOK,
		"rebound.example:" + port:       http.StatusForbidden,
		"localhost":                     http.StatusOK,
		"127.0.0.1:1":                   http.StatusOK, // a published port differs from the listen port
		"rebound.example":               http.StatusForbidden,
	} {
		request, _ := http.NewRequest("GET", server.URL+"/api/v1/status", nil)
		request.Host = host
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("Host %q: status %d, want %d", host, response.StatusCode, want)
		}
	}
}

type rawAnswer struct {
	Channel  string          `json:"channel"`
	Sequence uint64          `json:"sequence"`
	Status   Verification    `json:"status"`
	Reason   string          `json:"reason"`
	Size     int             `json:"size"`
	Raw      json.RawMessage `json:"raw"`
	RawText  string          `json:"raw_text"`
}

func TestRawMessageIsTheRecordAsStored(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	record := alice.sign(t, bus.GeneralChannel(), "hello")
	fake.store(bus.GeneralChannel(), 1, record)
	fake.store(bus.GeneralChannel(), 2, []byte("not json"))
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 2)
	server := serve(monitor)
	defer server.Close()

	var signed rawAnswer
	getJSON(t, server, "/api/v1/messages/raw?channel=general&sequence=1", &signed)
	if signed.Channel != "general" || signed.Sequence != 1 || signed.Status != Verified || signed.Size != len(record) {
		t.Errorf("answer = %+v", signed)
	}
	var got, want any
	if err := json.Unmarshal(signed.Raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(record, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("raw = %s, want the published payload %s", signed.Raw, record)
	}

	var broken rawAnswer
	getJSON(t, server, "/api/v1/messages/raw?channel=general&sequence=2", &broken)
	if broken.Status != Rejected || broken.Reason == "" || broken.RawText != "not json" || broken.Raw != nil || broken.Size != len("not json") {
		t.Errorf("answer = %+v", broken)
	}
}

func TestRawMessageOfADirectChannelHasBothStreams(t *testing.T) {
	alice, bob := newTestAgent("api-client@alice"), newTestAgent("lib-x@bob")
	fake := newFakeBus(t, alice, bob)
	toBob, toAlice := alice.sign(t, bus.DMChannel(bob.id), "ping"), bob.sign(t, bus.DMChannel(alice.id), "pong")
	fake.store(bus.DMChannel(bob.id), 3, toBob)
	fake.store(bus.DMChannel(alice.id), 5, toAlice)
	monitor := runMonitor(t, fake)
	const dm = "dm:api-client@alice:lib-x@bob"
	waitForMessages(t, monitor, dm, 2)
	server := serve(monitor)
	defer server.Close()

	for sequence, record := range map[int][]byte{3: toBob, 5: toAlice} {
		var answer rawAnswer
		path := "/api/v1/messages/raw?channel=" + url.QueryEscape(dm) + "&sequence=" + strconv.Itoa(sequence)
		getJSON(t, server, path, &answer)
		if answer.Channel != dm || answer.Size != len(record) || len(answer.Raw) == 0 {
			t.Errorf("sequence %d: answer = %+v", sequence, answer)
		}
	}
}

func TestRawMessageErrors(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "hello"))
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 1)
	server := serve(monitor)
	defer server.Close()

	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/api/v1/messages/raw?channel=general&sequence=9":  {http.StatusNotFound, "unknown_message"},
		"/api/v1/messages/raw?channel=nowhere&sequence=1":  {http.StatusNotFound, "unknown_message"},
		"/api/v1/messages/raw?channel=general":             {http.StatusBadRequest, "bad_request"},
		"/api/v1/messages/raw?channel=general&sequence=x":  {http.StatusBadRequest, "bad_request"},
		"/api/v1/messages/raw?channel=general&sequence=-1": {http.StatusBadRequest, "bad_request"},
	} {
		response, body := call(t, server, "GET", path, "", nil)
		if response.StatusCode != want.status || errorCode(body) != want.code {
			t.Errorf("%s: %d %s, want %d %s", path, response.StatusCode, body, want.status, want.code)
		}
	}
}

func TestMessageByIDFindsItInAnyChannel(t *testing.T) {
	alice, bob := newTestAgent("api-client@alice"), newTestAgent("lib-x@bob")
	fake := newFakeBus(t, alice, bob)
	general, direct := alice.sign(t, bus.GeneralChannel(), "hello"), bob.sign(t, bus.DMChannel(alice.id), "pong")
	fake.store(bus.GeneralChannel(), 4, general)
	fake.store(bus.DMChannel(alice.id), 2, direct)
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 1)
	waitForMessages(t, monitor, "dm:api-client@alice:lib-x@bob", 1)
	server := serve(monitor)
	defer server.Close()

	for _, channel := range []string{"general", "dm:api-client@alice:lib-x@bob"} {
		stored := monitor.Messages(channel, 0, 10)[0]
		var got Message
		getJSON(t, server, "/api/v1/messages/by-id?id="+stored.ID, &got)
		if got.ID != stored.ID || got.Channel != channel || got.Sequence != stored.Sequence || got.Text != stored.Text {
			t.Errorf("%s: answer = %+v, want %+v", channel, got, stored)
		}
	}
	for _, path := range []string{"/api/v1/messages/by-id?id=nothing", "/api/v1/messages/by-id"} {
		response, body := call(t, server, "GET", path, "", nil)
		if response.StatusCode != http.StatusNotFound || errorCode(body) != "unknown_message" {
			t.Errorf("%s: %d %s, want 404 unknown_message", path, response.StatusCode, body)
		}
	}
}

func TestMessagesCarryNoRawBytes(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "hello"))
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 1)
	server := serve(monitor)
	defer server.Close()

	_, body := call(t, server, "GET", "/api/v1/messages?channel=general", "", nil)
	for _, leaked := range []string{`"sig"`, `"envelope"`, `"raw"`, `"key_fp"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("/messages leaks %s: %s", leaked, body)
		}
	}
}
