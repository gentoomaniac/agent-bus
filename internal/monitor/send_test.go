package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// fakeSender records what the monitor sends.
type fakeSender struct {
	ready   error
	refuse  error    // what Send and Publish fail with
	sent    []string // "<destination>: <text>"
	targets [][]string
}

func (*fakeSender) AgentID() string     { return "monitor@alice" }
func (sender *fakeSender) Ready() error { return sender.ready }

func (sender *fakeSender) Send(_ context.Context, to string, message adapter.Outgoing) (string, error) {
	sender.sent = append(sender.sent, "agent "+to+": "+message.Body.Text)
	sender.targets = append(sender.targets, message.Targets)
	return "ID1", sender.refuse
}

func (sender *fakeSender) Publish(_ context.Context, channel string, message adapter.Outgoing) (string, error) {
	sender.sent = append(sender.sent, "group "+channel+": "+message.Body.Text)
	sender.targets = append(sender.targets, message.Targets)
	return "ID2", sender.refuse
}

// channelRequests are the channel requests the monitor made; each is answered as given.
func (sender *fakeSender) RequestChannel(_ context.Context, request channelapi.Request) (channelapi.Reply, error) {
	sender.sent = append(sender.sent, fmt.Sprintf("channel %s %s %s %s", request.Action, request.Channel, request.Agent, request.Description))
	if sender.refuse != nil {
		return channelapi.Reply{}, sender.refuse
	}
	channel := &channelapi.Channel{Name: request.Channel, Description: request.Description, Members: []string{"lib-x@bob"}}
	if request.Action == channelapi.RemoveMember {
		channel.Members = nil // the last member left: busd's answer has no members
	}
	return channelapi.Reply{Channel: channel}, nil
}

func (sender *fakeSender) Invite(ctx context.Context, channelName, agentID string) (channelapi.Reply, string, error) {
	reply, err := sender.RequestChannel(ctx, channelapi.Request{Action: channelapi.Invite, Channel: channelName, Agent: agentID})
	return reply, "", err
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
		"invalid target":      func(r *SendRequest) { r.Targets = []string{"Bob"} },
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

func TestSendPassesPickedTargetsAndLeavesMentionsToTheAdapter(t *testing.T) {
	sender := &fakeSender{}
	monitor := monitorWithSender(t, sender)
	request := SendRequest{To: envelope.To{Kind: envelope.ToGroup, Name: "general"}, Type: "request", Text: "@lib-x@bob please", Targets: []string{"lib-x@bob"}}
	if _, err := monitor.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Targets = []string{}
	if _, err := monitor.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(sender.targets) != 2 || len(sender.targets[0]) != 1 || sender.targets[1] != nil {
		t.Errorf("targets passed on = %v, want the picked one and then nil", sender.targets)
	}
}

func TestAMessageAimedAtNobodyIsTheUsersMistake(t *testing.T) {
	sender := &fakeSender{refuse: fmt.Errorf("%w: name a target or @all", adapter.ErrBadTargets)}
	monitor := monitorWithSender(t, sender)
	request := SendRequest{To: envelope.To{Kind: envelope.ToGroup, Name: "general"}, Type: "message", Text: "hi"}
	if _, err := monitor.Send(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("error %v, want ErrInvalidRequest", err)
	}
	request.Targets = []string{"who@ever"}
	if _, err := monitor.Send(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("an unregistered target: error %v, want ErrInvalidRequest", err)
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
		response, err := http.Post(server.URL+"/api/v1/send", contentType, bytes.NewBufferString(body))
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
