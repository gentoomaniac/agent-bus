//go:build integration

package itest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
)

const monitorAgent = "monitor@alice"

// consumerPositions is every read cursor of the bus: where it has delivered and acknowledged to,
// and what is pending (delivered stream, delivered consumer, ack floor, ack pending, pending).
func (h *harness) consumerPositions(t *testing.T) map[string][5]uint64 {
	t.Helper()
	ctx := timeout(t)
	stream, err := h.rawAdmin(t, broker.BusAdminUser).js.Stream(ctx, "BUS")
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string][5]uint64{}
	for info := range stream.ListConsumers(ctx).Info() {
		positions[info.Name] = [5]uint64{info.Delivered.Stream, info.Delivered.Consumer, info.AckFloor.Stream, uint64(info.NumAckPending), info.NumPending}
	}
	return positions
}

// serveMonitor serves the monitor on a test server that is closed with the test.
func serveMonitor(t *testing.T, web *monitor.Monitor) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	handler, err := web.Handler(monitor.Access{Listen: server.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// getJSON reads a JSON endpoint of the monitor.
func getJSON[T any](t *testing.T, server *httptest.Server, path string) T {
	t.Helper()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value T
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// waitForMonitorMessages polls a channel of the monitor until it shows n messages.
func waitForMonitorMessages(t *testing.T, server *httptest.Server, channel string, n int) []monitor.Message {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		messages := getJSON[[]monitor.Message](t, server, "/api/v1/messages?channel="+channel)
		if len(messages) >= n {
			return messages
		}
		if time.Now().After(deadline) {
			t.Fatalf("channel %s shows %d messages, want %d: %+v", channel, len(messages), n, messages)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestMonitor(t *testing.T) {
	h := startBus(t)
	h.register(t, monitorAgent)
	h.run(t, h.busctl, "--direct", "channel", "join", bob, "lib-x")
	aliceSession, _ := h.startAdapter(t, alice)
	bobSession, _ := h.startAdapter(t, bob, "lib-x")
	ctx := timeout(t)

	if _, err := aliceSession.Send(ctx, bob, say("dm hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := bobSession.Send(ctx, alice, say("dm answer")); err != nil {
		t.Fatal(err)
	}
	if _, err := aliceSession.Publish(ctx, "general", sayTo("general hello", envelope.TargetAll)); err != nil {
		t.Fatal(err)
	}
	if _, err := bobSession.Publish(ctx, "lib-x", sayTo("group hello", envelope.TargetAll)); err != nil {
		t.Fatal(err)
	}

	// A record whose text was changed after signing.
	key, err := identity.ReadPrivateKey(creds.AgentDir(h.configDir, alice))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := envelope.Sign(envelope.Envelope{
		Version: envelope.Version, ID: envelope.NewID(time.Now()), TS: time.Now().UTC(), From: alice, To: envelope.ToFor(bus.GeneralChannel()),
		Type: "message", Targets: []string{envelope.TargetAll}, Thread: "t", KeyFP: identity.Fingerprint(key.Public().(ed25519.PublicKey)), Body: envelope.Body{Text: "as signed"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(payload, []byte("as signed"), []byte("as forged"), 1)
	if _, err := h.transport(t, alice).Publish(ctx, bus.GeneralChannel(), "tampered", tampered); err != nil {
		t.Fatal(err)
	}

	before := h.consumerPositions(t)

	sender, _ := h.startAdapter(t, monitorAgent)
	web := monitor.New(monitor.Config{
		Admin: h.admin(t), History: h.transportOnAdmin(t), Sender: sender,
		Report: func(err error) { t.Errorf("monitor: %v", err) },
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { web.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	server := serveMonitor(t, web)

	t.Run("lists channels, agents and members", func(t *testing.T) {
		waitForMonitorMessages(t, server, "general", 2)
		var channels []string
		for _, channel := range getJSON[[]monitor.Channel](t, server, "/api/v1/channels") {
			channels = append(channels, channel.ID)
		}
		if want := []string{"general", "group:lib-x", "dm:" + alice + ":" + bob}; !slices.Equal(channels, want) {
			t.Errorf("channels = %v, want %v", channels, want)
		}
		var agents []string
		for _, agent := range getJSON[[]monitor.Agent](t, server, "/api/v1/agents") {
			agents = append(agents, agent.ID)
		}
		if want := []string{alice, bob, monitorAgent}; !slices.Equal(agents, want) {
			t.Errorf("agents = %v, want %v", agents, want)
		}
		for channel, want := range map[string][]string{
			"general":                 {alice, bob, monitorAgent},
			"group:lib-x":             {bob},
			"dm:" + alice + ":" + bob: {alice, bob},
		} {
			if members := getJSON[[]string](t, server, "/api/v1/members?channel="+channel); !slices.Equal(members, want) {
				t.Errorf("members of %s = %v, want %v", channel, members, want)
			}
		}
	})

	t.Run("shows its own identity as monitor", func(t *testing.T) {
		if view := presenceFor(t, server, monitorAgent); view.State != "monitor" {
			t.Errorf("presence of %s = %+v, want state monitor", monitorAgent, view)
		}
	})

	t.Run("shows both direct messages and the group post as verified", func(t *testing.T) {
		dm := waitForMonitorMessages(t, server, "dm:"+alice+":"+bob, 2)
		if dm[0].Text != "dm hello" || dm[1].Text != "dm answer" || dm[0].Sequence >= dm[1].Sequence {
			t.Errorf("DM conversation = %+v, want both messages in stream order", dm)
		}
		group := waitForMonitorMessages(t, server, "group:lib-x", 1)
		for _, message := range append(dm, group...) {
			if message.Status != monitor.Verified {
				t.Errorf("%+v: status %q, want verified", message, message.Status)
			}
		}
	})

	t.Run("shows general with the tampered record as rejected", func(t *testing.T) {
		general := waitForMonitorMessages(t, server, "general", 2)
		if len(general) != 2 || general[0].Status != monitor.Verified || general[0].Text != "general hello" {
			t.Fatalf("general = %+v", general)
		}
		if forged := general[1]; forged.Status != monitor.Rejected || !strings.Contains(forged.Reason, "does not verify") {
			t.Errorf("tampered record = %+v, want rejected as not verifying", forged)
		}
	})

	t.Run("leaves every read cursor where it was", func(t *testing.T) {
		after := h.consumerPositions(t)
		for name, position := range before {
			if after[name] != position {
				t.Errorf("consumer %s moved: %v -> %v", name, position, after[name])
			}
		}
		if pending := before[bus.AgentToken(bob)+"__dm"]; pending[4] == 0 {
			t.Errorf("bob's DM consumer has nothing pending (%v): the check proves nothing", pending)
		}
	})

	t.Run("sending reaches the agent's inbox and shows as verified", func(t *testing.T) {
		if status := getJSON[monitor.Status](t, server, "/api/v1/status"); !status.CanSend || status.Agent != monitorAgent {
			t.Fatalf("status = %+v", status)
		}
		request := `{"to":{"kind":"agent","name":"` + bob + `"},"type":"question","title":"from the monitor","text":"are you there?"}`
		response, err := http.Post(server.URL+"/api/v1/send", "application/json", strings.NewReader(request))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("send: status %d", response.StatusCode)
		}

		deadline := time.Now().Add(15 * time.Second)
		for {
			if err := bobSession.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			messages, err := bobSession.Inbox(adapter.InboxQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(messages, func(m adapter.Message) bool {
				return m.Envelope.From == monitorAgent && m.Envelope.Type == "question" && m.Envelope.Body.Title == "from the monitor"
			}) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("bob's inbox lacks the monitor's message: %v", summaries(messages))
			}
		}
		sent := waitForMonitorMessages(t, server, "dm:"+bob+":"+monitorAgent, 1)
		if sent[0].Status != monitor.Verified || sent[0].From != monitorAgent {
			t.Errorf("sent message = %+v, want verified from %s", sent[0], monitorAgent)
		}
	})
}
