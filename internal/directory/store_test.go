package directory

import (
	"context"
	"errors"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

func TestACommitWritesTheSnapshotAndItsExports(t *testing.T) {
	store := Store{Dir: trust.DirectoryPath(t.TempDir())}
	first, _ := twoRevisions()
	version := versionOf("e1", 1, first)

	written, err := store.Commit(Version{}, version, first)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil || loaded.Version != version || loaded.Hash() != first.Hash() || written.Version != version {
		t.Errorf("loaded %s (%v), want %s", loaded.Version, err, version)
	}
	if record, err := store.Dir.Lookup(context.Background(), "bob@x"); err != nil || record.Fingerprint != "fb" {
		t.Errorf("bob's export: %+v, %v", record, err)
	}
	if catalogue, err := store.Dir.Catalogue(); err != nil || len(catalogue["team"]) != 1 {
		t.Errorf("the catalogue's export: %v, %v", catalogue, err)
	}
	if records, err := store.Dir.Records(context.Background()); err != nil || len(records) != 2 {
		t.Errorf("the directory lists %d records (%v): the snapshot and its lock must not count", len(records), err)
	}
}

func TestACommitOntoAnotherBaseWritesNothing(t *testing.T) {
	store := Store{Dir: trust.DirectoryPath(t.TempDir())}
	first, second := twoRevisions()
	if _, err := store.Commit(Version{}, versionOf("e1", 1, first), first); err != nil {
		t.Fatal(err)
	}

	_, err := store.Commit(Version{}, versionOf("e1", 2, second), second)

	if !errors.Is(err, ErrMoved) {
		t.Errorf("a commit onto a base that moved: %v, want ErrMoved", err)
	}
	if loaded, _ := store.Load(); loaded.Version.Revision != 1 {
		t.Errorf("the snapshot moved to %s", loaded.Version)
	}
}

func TestRevocationsOutliveTheRecordsThatNamedThem(t *testing.T) {
	store := Store{Dir: trust.DirectoryPath(t.TempDir())}
	first, second := twoRevisions() // bob's key is revoked in the second
	if _, err := store.Commit(Version{}, versionOf("e1", 2, second), second); err != nil {
		t.Fatal(err)
	}

	// A restored busd of a new epoch offers bob's record from before his removal.
	written, err := store.Commit(versionOf("e1", 2, second), versionOf("e2", 1, first), first)
	if err != nil {
		t.Fatal(err)
	}

	if written.Revoked["fb"] != "bob@x" {
		t.Errorf("revoked = %v, want bob's key kept", written.Revoked)
	}
	exported, err := store.Dir.Lookup(context.Background(), "bob@x")
	if err != nil || !exported.Keys[0].Revoked {
		t.Errorf("bob's export: %+v, %v; want his key revoked", exported.Keys, err)
	}
	if written.Hash() != first.Hash() {
		t.Error("the snapshot's projection is not busd's: its hash no longer checks")
	}
}

func TestNextAddsTheRevocationsOfTheNewProjection(t *testing.T) {
	_, second := twoRevisions()
	known := Snapshot{Revoked: map[string]string{"old": "gone@x"}}

	next := known.Next(versionOf("e1", 2, second), second)

	if len(next.Revoked) != 2 || next.Revoked["old"] != "gone@x" || next.Revoked["fb"] != "bob@x" {
		t.Errorf("revoked = %v", next.Revoked)
	}
	if len(known.Revoked) != 1 {
		t.Error("Next changed the snapshot it was called on")
	}
}
