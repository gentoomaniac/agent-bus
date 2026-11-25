package adapter

import (
	"context"
	"slices"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

func TestAGroupJoinedOrLeftIsReadFromTheNextSyncOn(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	registry := trust.DirectoryPath(b.configDir)
	setReaders := func(readers ...string) {
		t.Helper()
		if err := registry.WriteCatalogue(trust.Catalogue{"team": readers}); err != nil {
			t.Fatal(err)
		}
	}
	setReaders(alice)
	busd := newFakeBusd(t, registry)
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	aliceAdapter, _ := b.open(alice)
	ctx := context.Background()
	sync := func() {
		t.Helper()
		busd.publish()
		if err := bobAdapter.SyncDirectory(ctx); err != nil {
			t.Fatal(err)
		}
	}
	team := bus.GroupChannel("team")

	sync()
	if slices.Contains(bobAdapter.Channels(), team) {
		t.Fatal("bob reads team before he joined it")
	}

	setReaders(alice, bob)
	sync()
	if !slices.Contains(bobAdapter.Channels(), team) {
		t.Fatalf("after joining, bob reads %v", bobAdapter.Channels())
	}
	if _, err := aliceAdapter.Publish(ctx, "team", toAll("standup")); err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if inbox := inboxOf(t, bobAdapter); len(inbox) != 1 || inbox[0].Channel != team {
		t.Errorf("bob's inbox = %+v, want the post in team", inbox)
	}

	setReaders(alice)
	sync()
	if slices.Contains(bobAdapter.Channels(), team) {
		t.Errorf("after leaving, bob still reads %v", bobAdapter.Channels())
	}
}
