package monitor

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

// fakeBus is an admin and a transport in one: a registry, a catalogue and the stored records,
// keyed by the channel's name.
type fakeBus struct {
	bus.Admin
	bus.Transport

	mu      sync.Mutex
	agents  []bus.AgentRecord
	groups  []bus.TopicRecord
	holders map[string][]string
	records map[string][]bus.Record
	down    error // what Unavailable returns

	presence map[string][]byte
	watchers []func(agentID string, record []byte)
}

func (fake *fakeBus) Presence(_ context.Context, agentID string) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.presence[agentID], nil
}

func (fake *fakeBus) WatchPresence(handler func(agentID string, record []byte)) (func(), error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.watchers = append(fake.watchers, handler)
	return func() {}, nil
}

// publishPresence stores the record and tells the watchers, as the broker does.
func (fake *fakeBus) publishPresence(agentID string, record []byte) {
	fake.mu.Lock()
	fake.presence[agentID] = record
	watchers := slices.Clone(fake.watchers)
	fake.mu.Unlock()
	for _, watcher := range watchers {
		watcher(agentID, record)
	}
}

func (*fakeBus) Close() {}

func (fake *fakeBus) Unavailable() error { return fake.down }

func (fake *fakeBus) Agents(context.Context) ([]bus.AgentRecord, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.agents), nil
}

func (fake *fakeBus) GroupChannels(context.Context) ([]bus.TopicRecord, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.groups), nil
}

func (fake *fakeBus) ReadCursorHolders(_ context.Context, channel bus.Channel) ([]string, error) {
	return fake.holders[channel.String()], nil
}

func (fake *fakeBus) History(_ context.Context, from bus.Channel, after uint64, limit int) ([]bus.Record, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var records []bus.Record
	for _, record := range fake.records[from.String()] {
		if record.Sequence > after && len(records) < limit {
			records = append(records, record)
		}
	}
	return records, nil
}

func (fake *fakeBus) store(channel bus.Channel, sequence uint64, payload []byte) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.records[channel.String()] = append(fake.records[channel.String()], bus.Record{Sequence: sequence, Payload: payload})
}

// testAgent is a registered agent with its key.
type testAgent struct {
	id  string
	key ed25519.PrivateKey
	fp  string
}

func newFakeBus(t *testing.T, registered ...*testAgent) *fakeBus {
	t.Helper()
	fake := &fakeBus{holders: map[string][]string{}, records: map[string][]bus.Record{}, presence: map[string][]byte{}}
	for _, agent := range registered {
		public := agent.key.Public().(ed25519.PublicKey)
		fake.agents = append(fake.agents, bus.AgentRecord{
			AgentID: agent.id, Fingerprint: agent.fp, PublicKey: base64.StdEncoding.EncodeToString(public),
			Keys: []bus.KeyEntry{{PublicKey: base64.StdEncoding.EncodeToString(public), Fingerprint: agent.fp}},
		})
	}
	return fake
}

func newTestAgent(id string) *testAgent {
	public, private := identity.Generate()
	return &testAgent{id: id, key: private, fp: identity.Fingerprint(public)}
}

// sign returns the record the agent publishes to a channel, aimed at the targets: by default the
// recipient of a DM, or everyone.
func (agent *testAgent) sign(t *testing.T, to bus.Channel, text string, targets ...string) []byte {
	t.Helper()
	if len(targets) == 0 {
		targets = []string{envelope.TargetAll}
		if to.Kind == bus.DM {
			targets = []string{to.Name}
		}
	}
	payload, err := envelope.Sign(envelope.Envelope{
		Version: envelope.Version, ID: envelope.NewID(time.Now()), TS: time.Now().UTC(), From: agent.id, To: envelope.ToFor(to),
		Type: "message", Targets: targets, Thread: "t", KeyFP: agent.fp, Body: envelope.Body{Text: text},
	}, agent.key)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// runMonitor follows the fake bus until the test ends.
func runMonitor(t *testing.T, fake *fakeBus) *Monitor {
	t.Helper()
	monitor := New(Config{Admin: fake, History: fake, Report: func(err error) { t.Error(err) }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return monitor
}

// waitForMessages waits until the channel holds n messages.
func waitForMessages(t *testing.T, monitor *Monitor, channel string, n int) []Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		messages := monitor.Messages(channel, 0, 100)
		if len(messages) >= n {
			return messages
		}
		if time.Now().After(deadline) {
			t.Fatalf("channel %s has %d messages, want %d", channel, len(messages), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVerificationStatus(t *testing.T) {
	alice, bob, stranger := newTestAgent("api-client@alice"), newTestAgent("lib-x@bob"), newTestAgent("who@ever")
	fake := newFakeBus(t, alice, bob)
	general, bobDM := bus.GeneralChannel(), bus.DMChannel(bob.id)

	tampered := alice.sign(t, general, "original")
	tampered = []byte(strings.Replace(string(tampered), "original", "changed!", 1))
	fake.store(general, 1, alice.sign(t, general, "hello"))
	fake.store(general, 2, stranger.sign(t, general, "who am I"))
	fake.store(general, 3, tampered)
	fake.store(general, 4, alice.sign(t, bobDM, "meant for bob only")) // replayed into general
	fake.store(general, 5, []byte("not json"))

	messages := waitForMessages(t, runMonitor(t, fake), "general", 5)

	type verdict struct {
		status Verification
		reason bool
	}
	want := []verdict{{Verified, false}, {UnknownSender, true}, {Rejected, true}, {Rejected, true}, {Rejected, true}}
	for i, message := range messages {
		if message.Status != want[i].status || (message.Reason != "") != want[i].reason {
			t.Errorf("message %d: status %q, reason %q; want status %q, reason set: %v", message.Sequence, message.Status, message.Reason, want[i].status, want[i].reason)
		}
	}
	if messages[0].From != alice.id || messages[0].Text != "hello" {
		t.Errorf("first message = %+v", messages[0])
	}
}

func TestMessagesShowTheirTargets(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "a request", envelope.TargetAll))
	fake.store(bus.GeneralChannel(), 2, alice.sign(t, bus.GeneralChannel(), "a request", "lib-x@bob", "docs@carol"))

	messages := waitForMessages(t, runMonitor(t, fake), "general", 2)
	if messages[1].To != "general" {
		t.Errorf("destination = %q, want general", messages[1].To)
	}
	if !slices.Equal(messages[1].Targets, []string{"lib-x@bob", "docs@carol"}) {
		t.Errorf("targets = %v", messages[1].Targets)
	}
}

func TestChannelsMembersAndMergedDM(t *testing.T) {
	alice, bob := newTestAgent("api-client@alice"), newTestAgent("lib-x@bob")
	fake := newFakeBus(t, alice, bob)
	fake.groups = []bus.TopicRecord{{Name: "lib-x", Description: "discussion about lib-x"}}
	fake.holders["channel lib-x"] = []string{bob.id}

	fake.store(bus.DMChannel(bob.id), 3, alice.sign(t, bus.DMChannel(bob.id), "ping"))
	fake.store(bus.DMChannel(alice.id), 5, bob.sign(t, bus.DMChannel(alice.id), "pong"))
	fake.store(bus.DMChannel(bob.id), 7, alice.sign(t, bus.DMChannel(bob.id), "ping again"))
	fake.store(bus.GroupChannel("lib-x"), 4, bob.sign(t, bus.GroupChannel("lib-x"), "in the group"))

	monitor := runMonitor(t, fake)
	dm := waitForMessages(t, monitor, "dm:api-client@alice:lib-x@bob", 3)
	var texts []string
	for _, message := range dm {
		texts = append(texts, message.Text)
	}
	if !slices.Equal(texts, []string{"ping", "pong", "ping again"}) {
		t.Errorf("DM conversation = %v, want both agents' messages in stream order", texts)
	}
	if dm[0].From != alice.id || dm[0].To != bob.id || dm[1].From != bob.id || dm[1].To != alice.id {
		t.Errorf("DM directions = %s>%s, %s>%s", dm[0].From, dm[0].To, dm[1].From, dm[1].To)
	}
	if group := waitForMessages(t, monitor, "group:lib-x", 1); group[0].To != "lib-x" {
		t.Errorf("group destination = %q", group[0].To)
	}

	var ids []string
	for _, channel := range monitor.Channels() {
		ids = append(ids, channel.ID)
	}
	want := []string{"general", "group:lib-x", "dm:api-client@alice:lib-x@bob"}
	if !slices.Equal(ids, want) {
		t.Errorf("channels = %v, want %v", ids, want)
	}

	for channel, want := range map[string][]string{
		"general":                       {alice.id, bob.id},
		"group:lib-x":                   {bob.id},
		"dm:api-client@alice:lib-x@bob": {alice.id, bob.id},
	} {
		members, err := monitor.Members(context.Background(), channel)
		if err != nil || !slices.Equal(members, want) {
			t.Errorf("members of %s = %v, %v; want %v", channel, members, err, want)
		}
	}
	if _, err := monitor.Members(context.Background(), "group:nope"); err != ErrUnknownChannel {
		t.Errorf("members of an unknown group: error %v, want ErrUnknownChannel", err)
	}
}

func TestMessagesArePagedBySequence(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	for sequence := uint64(1); sequence <= 5; sequence++ {
		fake.store(bus.GeneralChannel(), sequence*10, alice.sign(t, bus.GeneralChannel(), "m"))
	}
	monitor := runMonitor(t, fake)
	waitForMessages(t, monitor, "general", 5)

	page := monitor.Messages("general", 20, 2)
	if len(page) != 2 || page[0].Sequence != 30 || page[1].Sequence != 40 {
		t.Errorf("page after 20 = %+v, want sequences 30 and 40", page)
	}
	if rest := monitor.Messages("general", 50, 2); len(rest) != 0 {
		t.Errorf("page after the last = %+v, want none", rest)
	}
}

func TestSubscriberGetsNewMessages(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	monitor := runMonitor(t, fake)
	messages, unsubscribe := monitor.Subscribe()
	defer unsubscribe()

	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "live"))
	select {
	case message := <-messages:
		if message.Text != "live" || message.Channel != "general" {
			t.Errorf("message = %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message arrived")
	}
}
