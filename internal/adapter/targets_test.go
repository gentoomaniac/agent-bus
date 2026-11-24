package adapter

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const carol = "docs@carol"

func deliverAll(config *Config) { config.Deliver = DeliverAll }

func aimedAt(messageType, content string, targets ...string) Outgoing {
	return Outgoing{Type: messageType, Body: envelope.Body{Text: content}, Targets: targets}
}

func publishTo(t *testing.T, from *Adapter, channel string, message Outgoing) string {
	t.Helper()
	id, err := from.Publish(context.Background(), channel, message)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func inboxIDs(t *testing.T, adapter *Adapter) []string {
	t.Helper()
	var ids []string
	for _, message := range inboxOf(t, adapter) {
		ids = append(ids, message.Envelope.ID)
	}
	return ids
}

// signedBy signs an envelope as the agent, for records the adapter itself would never send.
func signedBy(t *testing.T, b *testBus, agentID string, env envelope.Envelope) []byte {
	t.Helper()
	key, err := identity.ReadPrivateKey(filepath.Join(b.configDir, agentID))
	if err != nil {
		t.Fatal(err)
	}
	env.Version, env.ID, env.TS, env.From, env.Thread = envelope.Version, envelope.NewID(b.now), b.now.UTC(), agentID, "T"
	env.KeyFP = identity.Fingerprint(key.Public().(ed25519.PublicKey))
	payload, err := envelope.Sign(env, key)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestSendingFillsImplicitTargets(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	ctx := context.Background()

	dm, _ := a.Send(ctx, bob, text("psst"))
	question := publishTo(t, a, "general", Outgoing{Type: "question", Body: envelope.Body{Text: "who knows?"}})
	mentioned := publishTo(t, a, "general", text("@docs@carol fyi"))
	poll(t, bobAdapter)
	reply := publishTo(t, bobAdapter, "general", Outgoing{Type: "answer", Body: envelope.Body{Text: "me"}, ReplyTo: question})
	explicit := publishTo(t, bobAdapter, "general", Outgoing{Type: "answer", Body: envelope.Body{Text: "me too"}, ReplyTo: question, Targets: []string{carol}})
	toAllReply := publishTo(t, bobAdapter, "general", Outgoing{Type: "answer", Body: envelope.Body{Text: "hello"}, ReplyTo: question, Targets: []string{envelope.TargetAll}})
	// Without reply_to, a parent inferred from recent reading binds an ack to its author, but a
	// new question is a new topic for @all.
	if _, err := bobAdapter.Inbox(InboxQuery{}); err != nil { // reading sets the recent parent
		t.Fatal(err)
	}
	inferredAck := publishTo(t, bobAdapter, "general", Outgoing{Type: "ack", Body: envelope.Body{Text: "on it"}})
	newQuestion := publishTo(t, bobAdapter, "general", Outgoing{Type: "question", Body: envelope.Body{Text: "unrelated?"}})

	want := map[string][]string{
		dm: {bob}, question: {envelope.TargetAll}, mentioned: {carol},
		reply: {alice}, explicit: {carol, alice}, toAllReply: {envelope.TargetAll},
		inferredAck: {alice}, newQuestion: {envelope.TargetAll},
	}
	for id, targets := range want {
		var got []string
		for _, record := range b.broker.records {
			signed, err := envelope.Parse(record.payload)
			if err != nil {
				t.Fatal(err)
			}
			if signed.Envelope.ID == id {
				got = signed.Envelope.Targets
			}
		}
		if !slices.Equal(got, targets) {
			t.Errorf("message %s: targets %v, want %v", id, got, targets)
		}
	}
}

func TestSendingToNobodyIsRefused(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	ctx := context.Background()

	for name, message := range map[string]Outgoing{
		"chatter in a channel":  text("anyone around?"),
		"an empty list":         aimedAt("message", "x", []string{}...),
		"an invalid ID":         aimedAt("message", "x", "nobody"),
		"an unregistered agent": aimedAt("message", "x", carol),
		"all with an ID":        aimedAt("message", "x", envelope.TargetAll, bob),
		"a duplicate":           aimedAt("message", "x", bob, bob),
	} {
		if _, err := a.Publish(ctx, "general", message); !errors.Is(err, ErrBadTargets) {
			t.Errorf("%s: error %v, want ErrBadTargets", name, err)
		}
	}
	if _, err := a.Send(ctx, bob, aimedAt("message", "x", carol)); !errors.Is(err, ErrBadTargets) {
		t.Errorf("a DM aimed at another agent: error %v", err)
	}
	if len(b.broker.records) != 0 {
		t.Errorf("%d refused messages were sent", len(b.broker.records))
	}
}

func TestTargetedModeDropsWhatDoesNotTargetMe(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob)

	toOther := publishTo(t, a, "general", aimedAt("message", "x", carol))
	direct, _ := a.Send(context.Background(), bob, text("psst"))
	targeted := publishTo(t, a, "general", aimedAt("request", "do xyz", bob))
	everyone := publishTo(t, a, "general", aimedAt("message", "hi", envelope.TargetAll))
	mentioned := publishTo(t, a, "general", text("@lib-x@bob the text names the target"))
	poll(t, bobAdapter)

	got := inboxIDs(t, bobAdapter)
	want := []string{direct, targeted, everyone, mentioned}
	if len(got) != len(want) {
		t.Fatalf("inbox = %v, want the DM, the targeted, @all and the mentioned message", got)
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			t.Errorf("%s is missing from the inbox %v", id, got)
		}
	}
	if bobTransport.acks != 1 {
		t.Errorf("the dropped message was acked %d times, want 1", bobTransport.acks)
	}
	if _, handled := bobAdapter.handled.messageByID(toOther); !handled {
		t.Error("the dropped message is not in the handled set")
	}
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != len(want) {
		t.Error("a dropped message came back")
	}
}

func TestDeliverAllKeepsMessagesToOthersInTheInbox(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	toOther := publishTo(t, a, "general", aimedAt("message", "x", carol))
	poll(t, bobAdapter)

	messages := inboxOf(t, bobAdapter)
	if len(messages) != 1 || messages[0].Envelope.ID != toOther || messages[0].TargetsMe {
		t.Errorf("inbox = %+v", messages)
	}
}

func TestReplyReachesTheAuthorOfTheMessageItAnswers(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob)

	question := publishTo(t, bobAdapter, "general", Outgoing{Type: "question", Body: envelope.Body{Text: "who knows?"}})
	poll(t, a)
	reply := publishTo(t, a, "general", Outgoing{Type: "ack", Body: envelope.Body{Text: "me"}, ReplyTo: question})
	poll(t, bobAdapter)
	if got := inboxIDs(t, bobAdapter); !slices.Equal(got, []string{reply}) {
		t.Errorf("inbox = %v, want the reply to bob's question", got)
	}
}

func TestIncomingDMWithoutItsRecipientAsTargetIsRejected(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	bobAdapter, bobTransport := b.open(bob)
	payload := signedBy(t, b, alice, envelope.Envelope{
		To: envelope.To{Kind: envelope.ToAgent, Name: bob}, Targets: []string{alice}, Type: "message", Body: envelope.Body{Text: "x"},
	})
	b.broker.records = append(b.broker.records, fakeRecord{bus.DMChannel(bob), 1, payload})
	poll(t, bobAdapter)
	if len(inboxOf(t, bobAdapter)) != 0 || bobTransport.acks != 1 {
		t.Error("a DM with foreign targets was delivered or not acked")
	}
}

func TestHooksTellOnlyOfMailThatTargetsTheAgent(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	publishTo(t, a, "general", aimedAt("message", "for carol", carol))
	request := publishTo(t, a, "general", aimedAt("request", "do xyz", bob))
	poll(t, bobAdapter)

	fresh, unread, err := bobAdapter.NewMail()
	if err != nil || len(fresh) != 1 || fresh[0].Envelope.ID != request || unread != 2 {
		t.Errorf("new mail = %v, %d unread, %v", fresh, unread, err)
	}
	summary, err := bobAdapter.UnreadSummary()
	if err != nil || len(summary) != 1 || summary[0].Envelope.ID != request {
		t.Errorf("summary = %v, %v", summary, err)
	}
}

func TestInboxListsTargetedMailFirstAndLimitsAfterwards(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	publishTo(t, a, "general", aimedAt("message", "for carol", carol))
	first := publishTo(t, a, "general", aimedAt("request", "one", bob))
	second := publishTo(t, a, "general", aimedAt("request", "two", bob))
	poll(t, bobAdapter)

	messages, err := bobAdapter.Inbox(InboxQuery{Limit: 2})
	if err != nil || len(messages) != 2 || messages[0].Envelope.ID != first || messages[1].Envelope.ID != second {
		t.Errorf("inbox = %v, %v", messages, err)
	}
	if bobAdapter.recent.id != second {
		t.Errorf("the reply parent is %s, want the newest targeted message %s", bobAdapter.recent.id, second)
	}
	all := inboxOf(t, bobAdapter)
	if len(all) != 3 || all[2].TargetsMe {
		t.Errorf("untargeted mail should list last: %v", all)
	}
}

func TestContextOverflowDropsTheOldestContextAndNeverBlocksTargetedMail(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	a, _ := b.open(alice, func(config *Config) { config.RateLimit = 100 })
	bobAdapter, bobTransport := b.open(bob, deliverAll, func(config *Config) { config.ContextMessages = 3; config.InboxMessages = 4 })
	var chatter []string
	for i := 0; i < 6; i++ {
		chatter = append(chatter, publishTo(t, a, "general", aimedAt("message", "chatter", carol)))
	}
	for i := 0; i < 3; i++ {
		poll(t, bobAdapter)
	}
	if got := inboxIDs(t, bobAdapter); !slices.Equal(got, chatter[3:]) {
		t.Errorf("inbox = %v, want the newest 3 context messages", got)
	}
	if bobTransport.acks != 3 || bobAdapter.TakeContextDropped() != 3 || bobAdapter.TakeContextDropped() != 0 {
		t.Errorf("acks %d: the dropped context was not acked, or its count is wrong", bobTransport.acks)
	}
	request := publishTo(t, a, "general", aimedAt("request", "do it", bob))
	poll(t, bobAdapter)
	if got := inboxIDs(t, bobAdapter); got[0] != request {
		t.Errorf("inbox = %v, want the request first", got)
	}
}

func TestCatalogueSnapshotGivesSubscriptionsAndRefusesUnknownChannels(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.register(carol)
	catalogue := trust.Catalogue{"dev": {alice, bob}, "ops": {bob}, "empty": {}}
	if err := trust.DirectoryPath(b.configDir).WriteCatalogue(catalogue); err != nil {
		t.Fatal(err)
	}
	a, _ := b.open(alice, func(config *Config) { config.Groups = []string{"extra", "dev"} })
	var got []string
	for _, channel := range a.Channels() {
		got = append(got, channel.String())
	}
	if want := []string{"DM " + alice, "general", "channel dev", "channel extra"}; !slices.Equal(got, want) {
		t.Errorf("channels = %v, want %v", got, want)
	}

	ctx := context.Background()
	if _, err := a.Publish(ctx, "nowhere", toAll("x")); err == nil || !strings.Contains(err.Error(), "unknown channel") {
		t.Errorf("publish to an unknown channel: %v", err)
	}
	if _, err := a.Publish(ctx, "ops", toAll("x")); err != nil {
		t.Errorf("publish to a catalogue channel the agent does not read: %v", err)
	}
	if _, err := a.Publish(ctx, "extra", toAll("x")); err != nil {
		t.Errorf("publish to a channel read through Groups: %v", err)
	}
	if got := a.NonReaders("dev", []string{bob, carol, envelope.TargetAll}); !slices.Equal(got, []string{carol}) {
		t.Errorf("non-readers of dev = %v, want carol", got)
	}
	if got := a.NonReaders("general", []string{carol}); got != nil {
		t.Errorf("everyone reads general, got %v", got)
	}
	if got := a.NonReaders("extra", []string{carol}); got != nil {
		t.Errorf("a channel outside the catalogue says nothing, got %v", got)
	}
}
