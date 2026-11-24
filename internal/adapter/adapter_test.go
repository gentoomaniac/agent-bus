package adapter

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	alice = "api-client@alice"
	bob   = "lib-x@bob"
)

// fakeBroker is a message log that redelivers whatever an agent has not acked, as if AckWait were
// zero. Its sequences are global, like the stream's.
type fakeBroker struct {
	records []fakeRecord
	acked   map[string]bool // agent|channel|sequence
}

type fakeRecord struct {
	channel  bus.Channel
	sequence uint64
	payload  []byte
}

type fakeTransport struct {
	broker *fakeBroker
	agent  string
	acks   int
}

func (broker *fakeBroker) transport(agent string) *fakeTransport {
	return &fakeTransport{broker: broker, agent: agent}
}

func (transport *fakeTransport) Publish(_ context.Context, to bus.Channel, _ string, payload []byte) (uint64, error) {
	broker := transport.broker
	sequence := uint64(len(broker.records) + 1)
	broker.records = append(broker.records, fakeRecord{to, sequence, payload})
	return sequence, nil
}

func (transport *fakeTransport) Fetch(_ context.Context, from bus.Channel, max int) ([]bus.Delivery, error) {
	var deliveries []bus.Delivery
	for _, record := range transport.broker.records {
		key := func() string {
			return transport.agent + "|" + record.channel.String() + "|" + string(rune(record.sequence))
		}
		if record.channel != from || transport.broker.acked[key()] || len(deliveries) == max {
			continue
		}
		deliveries = append(deliveries, bus.Delivery{
			Record: bus.Record{Sequence: record.sequence, Payload: record.payload},
			Ack: func(context.Context) error {
				if transport.broker.acked == nil {
					transport.broker.acked = map[string]bool{}
				}
				transport.broker.acked[key()] = true
				transport.acks++
				return nil
			},
			InProgress: func(context.Context) error { return nil },
		})
	}
	return deliveries, nil
}

func (transport *fakeTransport) History(context.Context, bus.Channel, uint64, int) ([]bus.Record, error) {
	return nil, errors.New("not needed")
}

func (transport *fakeTransport) Close() {}

// testBus is a config directory with a registry directory and a clock the tests can move.
type testBus struct {
	t         *testing.T
	configDir string
	broker    *fakeBroker
	now       time.Time
}

func newTestBus(t *testing.T) *testBus {
	return &testBus{t: t, configDir: t.TempDir(), broker: &fakeBroker{}, now: time.Now()}
}

// register makes an identity: its key on disk and its record in the registry directory.
func (b *testBus) register(agentID string) {
	b.t.Helper()
	public, private := identity.Generate()
	dir := filepath.Join(b.configDir, agentID)
	if err := identity.WritePrivateKey(dir, private); err != nil {
		b.t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(public)
	fingerprint := identity.Fingerprint(public)
	record := bus.AgentRecord{AgentID: agentID, PublicKey: key, Fingerprint: fingerprint, Keys: []bus.KeyEntry{{PublicKey: key, Fingerprint: fingerprint}}}
	if err := trust.DirectoryPath(b.configDir).Write(record); err != nil {
		b.t.Fatal(err)
	}
}

func (b *testBus) open(agentID string, mutate ...func(*Config)) (*Adapter, *fakeTransport) {
	b.t.Helper()
	transport := b.broker.transport(agentID)
	config := Config{
		AgentID: agentID, Dir: filepath.Join(b.configDir, agentID), Transport: transport,
		Directory: trust.DirectoryPath(b.configDir), Now: func() time.Time { return b.now },
	}
	for _, change := range mutate {
		change(&config)
	}
	adapter, err := Open(config)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { adapter.Close() })
	return adapter, transport
}

func text(content string) Outgoing {
	return Outgoing{Type: "message", Body: envelope.Body{Text: content}}
}

func poll(t *testing.T, adapter *Adapter) {
	t.Helper()
	if err := adapter.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func inboxOf(t *testing.T, adapter *Adapter) []Message {
	t.Helper()
	messages, err := adapter.Inbox(InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestDMAndGeneralPostAreVerifiedPinnedAndAcked(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob, deliverAll)
	ctx := context.Background()

	dmID, err := a.Send(ctx, bob, text("hello bob"))
	if err != nil {
		t.Fatal(err)
	}
	postID, err := a.Publish(ctx, "general", text("hello all"))
	if err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	messages := inboxOf(t, bobAdapter)
	if len(messages) != 2 || messages[0].Envelope.ID != dmID && messages[1].Envelope.ID != dmID {
		t.Fatalf("inbox = %+v", messages)
	}
	if !messages[0].FirstContact && !messages[1].FirstContact {
		t.Error("the first message from alice is not marked as a first contact")
	}
	if pins := bobAdapterPins(t, b, bob); len(pins) != 1 || pins[0].Source != trust.SourceTOFU {
		t.Errorf("bob's pins of alice = %+v", pins)
	}

	if err := bobAdapter.Ack(ctx, []string{dmID, postID}); err != nil {
		t.Fatal(err)
	}
	if len(inboxOf(t, bobAdapter)) != 0 || bobTransport.acks != 2 {
		t.Errorf("after ack: inbox %d, broker acks %d", len(inboxOf(t, bobAdapter)), bobTransport.acks)
	}
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 0 {
		t.Error("acked mail came back")
	}
}

func bobAdapterPins(t *testing.T, b *testBus, agentID string) []trust.Pin {
	t.Helper()
	store, err := trust.Open(filepath.Join(b.configDir, agentID, knownAgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	return store.Pins(alice)
}

func TestOwnPostsOnGeneralAreNotDeliveredBack(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	a, transport := b.open(alice)
	if _, err := a.Publish(context.Background(), "general", text("hi")); err != nil {
		t.Fatal(err)
	}
	poll(t, a)
	if len(inboxOf(t, a)) != 0 || transport.acks != 1 {
		t.Error("an own post was delivered back or not acked")
	}
}

func TestTamperedMessageIsRejectedAlertedAndAcked(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob, deliverAll)
	if _, err := a.Send(context.Background(), bob, text("pay 10")); err != nil {
		t.Fatal(err)
	}
	b.broker.records[0].payload = []byte(strings.Replace(string(b.broker.records[0].payload), "pay 10", "pay 99", 1))

	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 0 || bobTransport.acks != 1 {
		t.Error("a tampered message was delivered, or not acked")
	}
	alerts, _ := os.ReadFile(filepath.Join(b.configDir, bob, alertsFile))
	if !strings.Contains(string(alerts), "does not verify") {
		t.Errorf("alerts = %q", alerts)
	}
}

func TestMessageCopiedIntoAnotherChannelIsRejected(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register("someone@else")
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	if _, err := a.Send(context.Background(), "someone@else", text("meant for someone else")); err != nil {
		t.Fatal(err)
	}
	b.broker.records[0].channel = bus.DMChannel(bob)
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 0 {
		t.Error("a message addressed to another agent was delivered")
	}
}

func TestChangedKeyIsRejectedAndAlerted(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	ctx := context.Background()
	if _, err := a.Send(ctx, bob, text("first")); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 1 {
		t.Fatal("first message not delivered")
	}

	// Someone else signs as alice with a key bob never pinned.
	impostorDir := filepath.Join(t.TempDir(), alice)
	_, private := identity.Generate()
	if err := identity.WritePrivateKey(impostorDir, private); err != nil {
		t.Fatal(err)
	}
	impostor, err := Open(Config{AgentID: alice, Dir: impostorDir, Transport: b.broker.transport(alice), Directory: trust.DirectoryPath(b.configDir), Now: func() time.Time { return b.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer impostor.Close()
	if _, err := impostor.Send(ctx, bob, text("second")); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if got := inboxOf(t, bobAdapter); len(got) != 1 || got[0].Envelope.Body.Text != "first" {
		t.Errorf("inbox = %+v, want only the first message", got)
	}
	alerts, _ := os.ReadFile(filepath.Join(b.configDir, bob, alertsFile))
	if !strings.Contains(string(alerts), "changed") {
		t.Errorf("alerts = %q", alerts)
	}
}

func TestFutureTimestampIsRejected(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice, func(config *Config) { config.Now = func() time.Time { return b.now.Add(time.Hour) } })
	bobAdapter, _ := b.open(bob)
	if _, err := a.Send(context.Background(), bob, text("from the future")); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 0 {
		t.Error("a message from the future was delivered")
	}
}

func TestDuplicateRecordIsSkippedAndAcked(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob, deliverAll)
	ctx := context.Background()
	if _, err := a.Send(ctx, bob, text("once")); err != nil {
		t.Fatal(err)
	}
	b.broker.records = append(b.broker.records, fakeRecord{b.broker.records[0].channel, 2, b.broker.records[0].payload})

	poll(t, bobAdapter) // both records in one batch: the second finds the first in the buffer
	if len(inboxOf(t, bobAdapter)) != 1 {
		t.Fatalf("buffered duplicate delivered twice")
	}
	if err := bobAdapter.Ack(ctx, []string{b.idOfFirst(t)}); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter) // the copy comes back after the ack: handled set skips it
	if len(inboxOf(t, bobAdapter)) != 0 || bobTransport.acks != 2 {
		t.Errorf("after the ack the duplicate came back or was not acked (acks %d)", bobTransport.acks)
	}
}

func (b *testBus) idOfFirst(t *testing.T) string {
	t.Helper()
	signed, err := envelope.Parse(b.broker.records[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	return signed.Envelope.ID
}

func TestChannelsAreDMGeneralAndGroups(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	a, _ := b.open(alice, func(config *Config) { config.Groups = []string{"dev"} })
	want := []bus.Channel{bus.DMChannel(alice), bus.GeneralChannel(), bus.GroupChannel("dev")}
	if got := a.Channels(); !slices.Equal(got, want) {
		t.Errorf("Channels() = %v, want %v", got, want)
	}
}

func TestSecondAdapterOnTheSameIdentityFailsEveryOperation(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	first, _ := b.open(alice)
	second, _ := b.open(alice)
	ctx := context.Background()

	errs := map[string]error{}
	_, errs["send"] = second.Send(ctx, bob, text("x"))
	_, errs["publish"] = second.Publish(ctx, "general", text("x"))
	errs["poll"] = second.Poll(ctx)
	_, errs["inbox"] = second.Inbox(InboxQuery{})
	errs["ack"] = second.Ack(ctx, []string{"x"})
	errs["run"] = second.Run(ctx, func(error) {})
	errs["ready"] = second.Ready()
	for operation, err := range errs {
		var held identity.HeldError
		if !errors.As(err, &held) || !strings.Contains(err.Error(), alice) {
			t.Errorf("%s: %v, want an error naming the identity and holder", operation, err)
		}
	}
	if _, err := first.Publish(ctx, "general", text("still works")); err != nil {
		t.Errorf("the lock holder is affected: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Error(err)
	}
}

func TestSecretIsBlockedAndNothingIsSent(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	_, err := a.Send(context.Background(), bob, text("-----BEGIN OPENSSH PRIVATE KEY-----"))
	if err == nil || !strings.Contains(err.Error(), "private key") || len(b.broker.records) != 0 {
		t.Errorf("err = %v, %d records", err, len(b.broker.records))
	}
}

func TestRateLimit(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	a, _ := b.open(alice, func(config *Config) { config.RateLimit = 2 })
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := a.Publish(ctx, "general", text("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Publish(ctx, "general", text("x")); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("third send: %v", err)
	}
}

// reply reads the agent's inbox and answers the newest message without naming reply_to or thread.
func reply(t *testing.T, adapter *Adapter, to string, viaPublish bool) error {
	t.Helper()
	poll(t, adapter)
	messages := inboxOf(t, adapter)
	var err error
	if viaPublish {
		_, err = adapter.Publish(context.Background(), "general", text("reply"))
	} else {
		_, err = adapter.Send(context.Background(), to, text("reply"))
	}
	if len(messages) > 0 {
		if ackErr := adapter.Ack(context.Background(), []string{messages[len(messages)-1].Envelope.ID}); ackErr != nil {
			t.Fatal(ackErr)
		}
	}
	return err
}

func TestHopLimitStopsTwoAutoRepliers(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)
	if _, err := a.Send(context.Background(), bob, text("start")); err != nil {
		t.Fatal(err)
	}
	var stopped error
	sent := 1
	for turn := 0; turn < 20 && stopped == nil; turn++ {
		replier, to := bobAdapter, alice
		if turn%2 == 1 {
			replier, to = a, bob
		}
		if stopped = reply(t, replier, to, false); stopped == nil {
			sent++
		}
	}
	if stopped == nil || !strings.Contains(stopped.Error(), "limit") {
		t.Fatalf("the loop never stopped: %v", stopped)
	}
	if sent != defaultMaxHops+1 {
		t.Errorf("%d messages were exchanged, want %d (hops 0 to %d)", sent, defaultMaxHops+1, defaultMaxHops)
	}
}

func TestReplyToUnknownMessageIsRefusedAndExplicitReplyCountsHops(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	ctx := context.Background()
	if _, err := a.Send(ctx, bob, Outgoing{Type: "message", Body: envelope.Body{Text: "x"}, ReplyTo: "INVENTED"}); err == nil {
		t.Error("an invented reply_to was accepted")
	}
	first, _ := a.Send(ctx, bob, text("root"))
	b.now = b.now.Add(10 * time.Minute) // too old to be inherited
	second, err := a.Send(ctx, bob, text("not a reply"))
	if err != nil {
		t.Fatal(err)
	}
	third, err := a.Send(ctx, bob, Outgoing{Type: "answer", Body: envelope.Body{Text: "y"}, ReplyTo: first})
	if err != nil {
		t.Fatal(err)
	}
	hops := map[string]int{}
	for _, record := range b.broker.records {
		signed, _ := envelope.Parse(record.payload)
		hops[signed.Envelope.ID] = signed.Envelope.Hops
	}
	if hops[first] != 0 || hops[second] != 0 || hops[third] != 1 {
		t.Errorf("hops = %v", hops)
	}
}

func TestInboxBufferPausesFetchingWhenFull(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice, func(config *Config) { config.RateLimit = 100 })
	bobAdapter, _ := b.open(bob, func(config *Config) { config.InboxMessages = 3 })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := a.Send(ctx, bob, text("x")); err != nil {
			t.Fatal(err)
		}
	}
	poll(t, bobAdapter)
	poll(t, bobAdapter)
	messages := inboxOf(t, bobAdapter)
	if len(messages) != 3 {
		t.Fatalf("buffered %d messages with room for 3", len(messages))
	}
	if err := bobAdapter.Ack(ctx, []string{messages[0].Envelope.ID, messages[1].Envelope.ID, messages[2].Envelope.ID}); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if got := len(inboxOf(t, bobAdapter)); got != 2 {
		t.Errorf("after draining, %d of the remaining 2 arrived", got)
	}
}

func TestSendToAnUnknownAgentIsRefused(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	a, _ := b.open(alice)
	_, err := a.Send(context.Background(), bob, text("anyone there?"))
	if err == nil || !strings.Contains(err.Error(), "unknown agent") || len(b.broker.records) != 0 {
		t.Errorf("err = %v, %d records", err, len(b.broker.records))
	}
}
