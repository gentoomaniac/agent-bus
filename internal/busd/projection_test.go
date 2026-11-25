package busd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

func (f *fixture) directory() *Directory {
	ca, err := tlsinit.LoadCA(filepath.Join(f.dir, "ca.pem"), filepath.Join(f.dir, "ca.key"))
	if err != nil {
		f.t.Fatal(err)
	}
	return NewDirectory(DirectoryConfig{Source: f.registry, CA: ca, ConfigDir: f.dir})
}

// syncFrom asks published as an agent at since and returns the verified reply.
func (f *fixture) syncFrom(published *Directory, since directory.Version) directory.SyncReply {
	f.t.Helper()
	request := directory.NewSyncRequest(since)
	reply, err := directory.VerifySyncReply(mustRead(f.t, filepath.Join(f.dir, "ca.pem")), published.HandleSync(request), request)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply
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

func TestASyncIsAnsweredWithWhatTheAgentLacks(t *testing.T) {
	f := newFixture(t)
	f.register("alice@x", f.newKey())
	published := f.directory()
	ctx := context.Background()
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	first := published.Version()

	if reply := f.syncFrom(published, directory.Version{}); reply.Kind != directory.Full || len(reply.Records) != 1 || reply.Version() != first {
		t.Errorf("a new agent got %s with %d records at %s", reply.Kind, len(reply.Records), reply.Version())
	}
	if reply := f.syncFrom(published, first); reply.Kind != directory.Unchanged {
		t.Errorf("an agent at the current version got %s", reply.Kind)
	}

	f.register("bob@x", f.newKey())
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	reply := f.syncFrom(published, first)
	if reply.Kind != directory.Diff || len(reply.Records) != 1 || reply.Records[0].AgentID != "bob@x" || reply.Catalogue != nil {
		t.Errorf("an agent one revision behind got %s with %+v, catalogue %v", reply.Kind, reply.Records, reply.Catalogue)
	}

	// A restart forgets the earlier projections: one full sync, nothing worse.
	if reply := f.syncFrom(f.directory(), first); reply.Kind != directory.Full || reply.Version() != published.Version() {
		t.Errorf("after a restart an agent behind got %s at %s", reply.Kind, reply.Version())
	}
}

func TestASyncBeforeTheFirstProjectionAndAMalformedOneGetSignedErrors(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	caPEM := mustRead(t, filepath.Join(f.dir, "ca.pem"))
	request := directory.NewSyncRequest(directory.Version{})

	if _, err := directory.VerifySyncReply(caPEM, published.HandleSync(request), request); err == nil || !strings.Contains(err.Error(), "not built") {
		t.Errorf("a sync before the first projection: %v", err)
	}
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("not a request")
	if _, err := directory.VerifySyncReply(caPEM, published.HandleSync(garbage), garbage); err == nil || !strings.Contains(err.Error(), "not a sync request") {
		t.Errorf("a malformed request: %v", err)
	}
}

func TestSyncsPastTheLimitAreAnsweredWithoutASignature(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	published.limiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	f.syncFrom(published, directory.Version{})

	request := directory.NewSyncRequest(directory.Version{})
	_, err := directory.VerifySyncReply(mustRead(t, filepath.Join(f.dir, "ca.pem")), published.HandleSync(request), request)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("a sync past the limit: %v", err)
	}
}
