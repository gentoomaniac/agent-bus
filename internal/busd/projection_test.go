package busd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func (f *fixture) directory() *Directory {
	return NewDirectory(DirectoryConfig{Source: f.registry, ConfigDir: f.dir})
}

func TestTheRevisionAdvancesOnlyWhenTheContentChanges(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	published := f.directory()
	ctx := context.Background()

	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	first := published.Version()
	if first.Epoch == "" || first.Revision != 1 || first.StateHash == "" {
		t.Fatalf("the first version is %+v", first)
	}
	if changes, err := published.Reconcile(ctx); err != nil || changes != nil || published.Version() != first {
		t.Errorf("an unchanged registry: %v, %v, version %+v, want no change of %+v", changes, err, published.Version(), first)
	}

	f.register("bob@x", f.newKey())
	changes, err := published.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second := published.Version()
	if second.Epoch != first.Epoch || second.Revision != 2 || second.StateHash == first.StateHash {
		t.Errorf("after a change the version is %+v, after %+v", second, first)
	}
	if len(changes) != 1 || changes[0].AgentID != "bob@x" {
		t.Errorf("changes = %+v, want bob joined", changes)
	}
}

func TestARestartKeepsTheVersionAndALostStateStartsANewEpoch(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	ctx := context.Background()
	published := f.directory()
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	before := published.Version()

	restarted := f.directory()
	if restarted.Version() != before {
		t.Errorf("after a restart the version is %+v, want %+v", restarted.Version(), before)
	}
	if changes, _ := restarted.Reconcile(ctx); changes != nil || restarted.Version() != before {
		t.Errorf("a restart without changes made revision %+v", restarted.Version())
	}

	if err := os.WriteFile(filepath.Join(f.dir, enrollDir, directoryStateFile), []byte(`{"version":{"epoch":"e","revision":9,"state_hash":"forged"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lost := f.directory()
	if _, err := lost.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if lost.Version().Epoch == before.Epoch || lost.Version().Revision != 1 {
		t.Errorf("a state that does not match its hash gave %+v, want a new epoch", lost.Version())
	}
}

func TestAnUnreadableRegistryChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	published := f.directory()
	ctx := context.Background()
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	before := published.Version()
	f.registry.down = errors.New("kv unreachable")

	if _, err := published.Reconcile(ctx); err == nil {
		t.Error("an unreadable registry reconciled")
	}
	if published.Version() != before {
		t.Errorf("the version moved to %+v", published.Version())
	}
}

func TestAnApprovalTellsTheDirectory(t *testing.T) {
	f := newFixture(t)
	told := 0
	f.service.config.DirectoryChanged = func() { told++ }
	reply := f.submit("alice@x", f.newKey(), "")

	if result := f.sign(reply.Code, false); result.Error != "" {
		t.Fatal(result.Error)
	}
	if told != 1 {
		t.Errorf("the directory was told %d times, want once", told)
	}
}
