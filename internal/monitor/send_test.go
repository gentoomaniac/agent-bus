package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// fakeSender records what the monitor sends.
type fakeSender struct {
	ready error
	sent  []string // "<destination>: <text>"
}

func (*fakeSender) AgentID() string     { return "monitor@alice" }
func (sender *fakeSender) Ready() error { return sender.ready }

func (sender *fakeSender) Send(_ context.Context, to string, message adapter.Outgoing) (string, error) {
	sender.sent = append(sender.sent, "agent "+to+": "+message.Body.Text)
	return "ID1", nil
}

func (sender *fakeSender) Publish(_ context.Context, channel string, message adapter.Outgoing) (string, error) {
	sender.sent = append(sender.sent, "group "+channel+": "+message.Body.Text)
	return "ID2", nil
}

func monitorWithSender(t *testing.T, sender Sender) *Monitor {
	t.Helper()
	fake := newFakeBus(t, newTestAgent("lib-x@bob"))
	fake.groups = append(fake.groups, bus.TopicRecord{Name: "lib-x"})
	monitor := New(Config{Admin: fake, History: fake, Sender: sender, Report: func(err error) { t.Error(err) }})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); monitor.followers.Wait() })
	if err := monitor.discover(ctx); err != nil {
		t.Fatal(err)
	}
	return monitor
}

func TestSendValidation(t *testing.T) {
	sender := &fakeSender{}
	monitor := monitorWithSender(t, sender)
	valid := SendRequest{To: envelope.To{Kind: envelope.ToGroup, Name: "general"}, Type: "message", Text: "hi"}

	for name, change := range map[string]func(*SendRequest){
		"unknown type":        func(r *SendRequest) { r.Type = "key_rotation" },
		"empty message":       func(r *SendRequest) { r.Text = "" },
		"unknown group":       func(r *SendRequest) { r.To.Name = "nope" },
		"malformed agent":     func(r *SendRequest) { r.To = envelope.To{Kind: envelope.ToAgent, Name: "Bob"} },
		"unregistered agent":  func(r *SendRequest) { r.To = envelope.To{Kind: envelope.ToAgent, Name: "who@ever"} },
		"unknown destination": func(r *SendRequest) { r.To.Kind = "private" },
	} {
		request := valid
		change(&request)
		if _, err := monitor.Send(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: error %v, want ErrInvalidRequest", name, err)
		}
	}
	if len(sender.sent) != 0 {
		t.Errorf("refused requests were sent: %v", sender.sent)
	}

	for to, want := range map[envelope.To]string{
		{Kind: envelope.ToGroup, Name: "general"}:   "group general: hi",
		{Kind: envelope.ToGroup, Name: "lib-x"}:     "group lib-x: hi",
		{Kind: envelope.ToAgent, Name: "lib-x@bob"}: "agent lib-x@bob: hi",
	} {
		request := valid
		request.To = to
		if _, err := monitor.Send(context.Background(), request); err != nil {
			t.Errorf("send to %v: %v", to, err)
		}
		if got := sender.sent[len(sender.sent)-1]; got != want {
			t.Errorf("sent %q, want %q", got, want)
		}
	}
}

func TestStatusAndSendWithoutAnIdentity(t *testing.T) {
	readOnly := monitorWithSender(t, nil)
	if status := readOnly.Status(); status.CanSend || status.Note == "" {
		t.Errorf("read-only status = %+v", status)
	}
	request := SendRequest{To: envelope.To{Kind: envelope.ToGroup, Name: "general"}, Type: "message", Text: "hi"}
	if _, err := readOnly.Send(context.Background(), request); !errors.Is(err, ErrReadOnly) {
		t.Errorf("send without an identity: error %v, want ErrReadOnly", err)
	}

	held := monitorWithSender(t, &fakeSender{ready: errors.New("monitor@alice: identity is in use by process 42")})
	if status := held.Status(); status.CanSend || status.Agent != "monitor@alice" || status.Note == "" {
		t.Errorf("status of a held identity = %+v", status)
	}
	if _, err := held.Send(context.Background(), request); !errors.Is(err, ErrReadOnly) {
		t.Errorf("send with a held identity: error %v, want ErrReadOnly", err)
	}

	ready := monitorWithSender(t, &fakeSender{})
	if status := ready.Status(); !status.CanSend || status.Agent != "monitor@alice" {
		t.Errorf("status = %+v", status)
	}
}

func TestSendEndpoint(t *testing.T) {
	sender := &fakeSender{}
	server := serve(monitorWithSender(t, sender))
	defer server.Close()
	post := func(contentType, body string) (*http.Response, string) {
		response, err := http.Post(server.URL+"/api/send", contentType, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var reply bytes.Buffer
		reply.ReadFrom(response.Body)
		return response, reply.String()
	}

	response, body := post("application/json", `{"to":{"kind":"group","name":"general"},"type":"message","text":"hi"}`)
	var reply struct{ ID string }
	if err := json.Unmarshal([]byte(body), &reply); err != nil || response.StatusCode != http.StatusOK || reply.ID != "ID2" {
		t.Errorf("send: status %d, body %q", response.StatusCode, body)
	}
	for name, test := range map[string]struct {
		contentType, body string
		status            int
	}{
		"form post":      {"text/plain", `{"to":{"kind":"group","name":"general"},"type":"message","text":"hi"}`, http.StatusUnsupportedMediaType},
		"not json":       {"application/json", `nope`, http.StatusBadRequest},
		"unknown field":  {"application/json", `{"surprise":1}`, http.StatusBadRequest},
		"invalid values": {"application/json", `{"to":{"kind":"group","name":"nope"},"type":"message","text":"hi"}`, http.StatusBadRequest},
	} {
		if response, body := post(test.contentType, test.body); response.StatusCode != test.status {
			t.Errorf("%s: status %d (%s), want %d", name, response.StatusCode, body, test.status)
		}
	}
	if len(sender.sent) != 1 {
		t.Errorf("sent %v, want exactly the first message", sender.sent)
	}
}
