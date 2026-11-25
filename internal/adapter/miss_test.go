package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// bobWithAnEmptyDirectory is bob with no record of anybody. His fetch copies the registry
// directory, unless the test brings its own.
func (b *testBus) bobWithAnEmptyDirectory(fetches *int, fetch func(context.Context) error) (*Adapter, trust.FileDirectory) {
	own := trust.DirectoryPath(b.t.TempDir())
	if fetch == nil {
		fetch = b.copyingBusd(own, fetches)
	}
	bobAdapter, _ := b.open(bob, func(config *Config) { config.Directory, config.FetchDirectory = own, fetch })
	return bobAdapter, own
}

func (b *testBus) recordOf(agentID string) bus.AgentRecord {
	b.t.Helper()
	record, err := trust.DirectoryPath(b.configDir).Lookup(context.Background(), agentID)
	if err != nil {
		b.t.Fatal(err)
	}
	return record
}

func TestSendToAnAgentEnrolledLaterFetchesTheDirectory(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	fetches := 0
	bobAdapter, _ := b.bobWithAnEmptyDirectory(&fetches, nil)
	ctx := context.Background()

	if _, err := bobAdapter.Send(ctx, alice, text("hello")); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Errorf("%d fetches, want 1", fetches)
	}
	if _, err := bobAdapter.Send(ctx, alice, text("again")); err != nil || fetches != 1 {
		t.Errorf("a known agent: %v, %d fetches, want no more", err, fetches)
	}
}

func TestFetchesOnAMissAreRateLimited(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	fetches := 0
	bobAdapter, _ := b.bobWithAnEmptyDirectory(&fetches, nil)
	ctx := context.Background()

	for _, stranger := range []string{"carol@x", "dave@x", "erin@x"} {
		if _, err := bobAdapter.Send(ctx, stranger, text("hi")); !errors.Is(err, ErrBadTargets) {
			t.Fatalf("send to %s: %v, want ErrBadTargets", stranger, err)
		}
	}
	if fetches != 1 {
		t.Errorf("%d fetches for three misses at once, want 1", fetches)
	}
	b.now = b.now.Add(missFetchInterval)
	if _, err := bobAdapter.Send(ctx, "carol@x", text("hi")); !errors.Is(err, ErrBadTargets) || fetches != 2 {
		t.Errorf("after the interval: %v, %d fetches, want a second one", err, fetches)
	}
}

func TestAnUnreachableBusdOnlyLeavesTheAgentUnknown(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	bobAdapter, own := b.bobWithAnEmptyDirectory(nil, func(context.Context) error { return errors.New("busd is down") })
	if err := own.Write(b.recordOf(alice)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := bobAdapter.Send(ctx, alice, text("known")); err != nil {
		t.Errorf("an agent of the local directory could not be written to because busd is down: %v", err)
	}
	_, err := bobAdapter.Send(ctx, "carol@x", text("unknown"))
	if !errors.Is(err, ErrBadTargets) || !strings.Contains(err.Error(), "busd is down") {
		t.Errorf("an unknown agent: %v, want ErrBadTargets that says why the fetch failed", err)
	}
}

func TestAMessageFromAnAgentEnrolledLaterIsDeliveredAfterAFetch(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	aliceAdapter, _ := b.open(alice)
	fetches := 0
	bobAdapter, _ := b.bobWithAnEmptyDirectory(&fetches, nil)
	if _, err := aliceAdapter.Send(context.Background(), bob, text("hello bob")); err != nil {
		t.Fatal(err)
	}

	poll(t, bobAdapter)

	if inbox := inboxOf(t, bobAdapter); len(inbox) != 1 || inbox[0].Envelope.From != alice {
		t.Errorf("inbox = %+v, want alice's message", inbox)
	}
	if senders, _ := bobAdapter.UnknownSenders(); len(senders) != 0 || fetches != 1 {
		t.Errorf("unknown senders %v after %d fetches, want none after 1", senders, fetches)
	}
}
