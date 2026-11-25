package adapter

import (
	"context"
	"slices"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

func TestTheAgentIsToldOfNetDirectoryChangesOncePerVersion(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	registry := trust.DirectoryPath(b.configDir)
	busd := newFakeBusd(t, registry)
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	ctx := context.Background()
	sync := func() []string {
		t.Helper()
		busd.publish()
		if err := bobAdapter.SyncDirectory(ctx); err != nil {
			t.Fatal(err)
		}
		return bobAdapter.TakeDirectoryNotices()
	}

	if notices := sync(); !slices.Equal(notices, []string{"Directory loaded: 2 agents, 0 group channels."}) {
		t.Errorf("the first sync told %q, want a summary", notices)
	}
	if notices := sync(); len(notices) != 0 {
		t.Errorf("an unchanged directory told %q", notices)
	}

	b.register("carol@x")
	b.remove(alice)
	if err := registry.WriteCatalogue(trust.Catalogue{"team": {bob}}); err != nil {
		t.Fatal(err)
	}
	b.register("came-and-went@x")
	b.remove("came-and-went@x")
	want := "Directory updated: api-client@alice was removed; carol@x joined; channel team is new, and you read it."
	if notices := sync(); !slices.Equal(notices, []string{want}) {
		t.Errorf("told %q\nwant %q", notices, want)
	}
	if notices := bobAdapter.TakeDirectoryNotices(); len(notices) != 0 {
		t.Errorf("a notice was told twice: %q", notices)
	}
}

func TestANewEpochIsToldToo(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	bobAdapter.TakeDirectoryNotices()

	busd.restore(b.recordOf(bob))
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	// alice's record is missing from the restore, which never means she was removed.
	want := "busd's directory was restored (a new epoch); every pinned and revoked key is kept."
	if notices := bobAdapter.TakeDirectoryNotices(); !slices.Equal(notices, []string{want}) {
		t.Errorf("told %q, want %q", notices, want)
	}
}
