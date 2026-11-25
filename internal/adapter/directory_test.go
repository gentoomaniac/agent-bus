package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// fakeBusd is busd's published directory for the adapter: it publishes the test bus's registry
// directory, signed by a CA of its own, and answers syncs as busd does.
type fakeBusd struct {
	t          *testing.T
	ca         *tlsinit.CA
	caPEM      []byte
	registry   trust.FileDirectory
	version    directory.Version
	projection directory.Projection
	earlier    map[directory.Version]directory.Projection
	kinds      []string // of the replies given
	down       error
	badDiffs   bool // diffs carry a state hash they do not hash to
	onHead     func([]byte)
}

func newFakeBusd(t *testing.T, registry trust.FileDirectory) *fakeBusd {
	t.Helper()
	dir := t.TempDir()
	err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "b.pem"), BrokerKeyPath: filepath.Join(dir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	busd := &fakeBusd{t: t, ca: ca, caPEM: caPEM, registry: registry, earlier: map[directory.Version]directory.Projection{}}
	busd.publish()
	return busd
}

// publish reads the registry and makes a new revision if it changed.
func (busd *fakeBusd) publish() {
	busd.t.Helper()
	records, err := busd.registry.Records(context.Background())
	if err != nil {
		busd.t.Fatal(err)
	}
	catalogue, _ := busd.registry.Catalogue()
	projection := directory.Projection{Records: records, Catalogue: catalogue}.Normalised()
	if projection.Hash() == busd.version.StateHash {
		return
	}
	busd.earlier[busd.version] = busd.projection
	busd.version = directory.Version{Epoch: "epoch-1", Revision: busd.version.Revision + 1, StateHash: projection.Hash()}
	busd.projection = projection
}

// restore makes busd start a new epoch, as after losing its state, with the given records.
func (busd *fakeBusd) restore(records ...bus.AgentRecord) {
	busd.projection = directory.Projection{Records: records}.Normalised()
	busd.version = directory.Version{Epoch: "epoch-2", Revision: 1, StateHash: busd.projection.Hash()}
	busd.earlier = map[directory.Version]directory.Projection{}
}

func (busd *fakeBusd) head(version directory.Version) []byte {
	busd.t.Helper()
	head, err := directory.SignHead(busd.ca, version, nil)
	if err != nil {
		busd.t.Fatal(err)
	}
	return head
}

func (busd *fakeBusd) RequestDirectory(_ context.Context, request []byte) ([]byte, error) {
	if busd.down != nil {
		return nil, busd.down
	}
	parsed, err := directory.ParseSyncRequest(request)
	if err != nil {
		return nil, err
	}
	var base *directory.Projection
	if projection, found := busd.earlier[parsed.Since]; found {
		base = &projection
	}
	reply := directory.Answer(parsed.Since, busd.version, busd.projection, base)
	reply.RequestHash = directory.RequestHash(request)
	if busd.badDiffs && reply.Kind == directory.Diff {
		reply.StateHash = "not the hash of this diff"
	}
	busd.kinds = append(busd.kinds, reply.Kind)
	return directory.SignSyncReply(busd.ca, reply)
}

func (busd *fakeBusd) WatchDirectoryHeads(handler func([]byte)) (func(), error) {
	busd.onHead = handler
	return func() {}, nil
}

// syncing opens agentID with a directory of its own, empty at first, synced from busd.
func (b *testBus) syncing(agentID string, busd *fakeBusd, own trust.FileDirectory) *Adapter {
	b.t.Helper()
	session, _ := b.open(agentID, func(config *Config) {
		config.Directory, config.DirectoryLink, config.CACert = own, busd, busd.caPEM
	})
	return session
}

func (b *testBus) recordOf(agentID string) bus.AgentRecord {
	b.t.Helper()
	record, err := trust.DirectoryPath(b.configDir).Lookup(context.Background(), agentID)
	if err != nil {
		b.t.Fatal(err)
	}
	return record
}

// refusesKey says whether the trust store of agentID, as saved, refuses the key whatever the
// directory says.
func (b *testBus) refusesKey(agentID, owner, fingerprint string) bool {
	b.t.Helper()
	store, err := trust.Open(filepath.Join(b.configDir, agentID, knownAgentsFile))
	if err != nil {
		b.t.Fatal(err)
	}
	_, err = store.Resolve(context.Background(), trust.DirectoryPath(b.t.TempDir()), owner, fingerprint, b.now)
	return err != nil && strings.Contains(err.Error(), "revoked key")
}

func TestASyncTakesInNewAgentsAndTheirRevocations(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	aliceAdapter, _ := b.open(alice)
	b.remove(alice)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()

	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := own.Lookup(ctx, alice); err != nil {
		t.Errorf("alice's record was not taken in: %v", err)
	}
	if !b.refusesKey(bob, alice, aliceAdapter.Fingerprint()) {
		t.Error("alice's revoked key was not installed in the trust store")
	}
	snapshot, err := directory.Store{Dir: own}.Load()
	if err != nil || snapshot.Version != busd.version {
		t.Errorf("the snapshot is at %s, %v, want %s", snapshot.Version, err, busd.version)
	}
}

func TestALaterSyncTakesADiffOntoTheLocalSnapshot(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	b.register(alice)
	busd.publish()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if want := []string{directory.Full, directory.Diff, directory.Unchanged}; fmt.Sprint(busd.kinds) != fmt.Sprint(want) {
		t.Errorf("busd answered %v, want %v", busd.kinds, want)
	}
	if _, err := own.Lookup(ctx, alice); err != nil {
		t.Errorf("the diff did not bring alice: %v", err)
	}
}

func TestAFailedSyncLeavesTheLocalDirectory(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	if err := own.Write(b.recordOf(alice)); err != nil {
		t.Fatal(err)
	}
	busd.down = errors.New("busd is down")
	bobAdapter := b.syncing(bob, busd, own)

	if err := bobAdapter.SyncDirectory(context.Background()); err == nil || err.Error() != "busd is down" {
		t.Errorf("SyncDirectory = %v, want busd's error", err)
	}
	if agents, err := bobAdapter.Agents(context.Background()); err != nil || len(agents) != 1 {
		t.Errorf("the local directory stopped working: %v, %v", agents, err)
	}
}

func TestADirectoryNotSignedByTheCAIsRefused(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	impostor := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	bobAdapter, _ := b.open(bob, func(config *Config) {
		config.Directory, config.DirectoryLink, config.CACert = own, impostor, busd.caPEM
	})

	if err := bobAdapter.SyncDirectory(context.Background()); err == nil || !strings.Contains(err.Error(), "not signed by the CA") {
		t.Errorf("a directory of another CA: %v", err)
	}
	if _, err := own.Lookup(context.Background(), alice); err == nil {
		t.Error("a record of a directory that failed to verify was written")
	}
}

func TestARollbackWithinAnEpochIsRefused(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	b.register(alice)
	busd.publish()
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	current := busd.version

	// busd restored from a backup of the same epoch: revision 1 again.
	for version, projection := range busd.earlier {
		if version.Revision == 1 {
			busd.version, busd.projection = version, projection
		}
	}
	if err := bobAdapter.SyncDirectory(ctx); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Errorf("an older revision: %v, want a rollback refused", err)
	}
	if snapshot, _ := (directory.Store{Dir: own}).Load(); snapshot.Version != current {
		t.Errorf("the snapshot moved to %s", snapshot.Version)
	}
}

func TestANewEpochKeepsEveryRevocation(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	aliceAdapter, _ := b.open(alice)
	original := b.recordOf(alice)
	b.remove(alice)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	var logged []string
	bobAdapter, _ := b.open(bob, func(config *Config) {
		config.Directory, config.DirectoryLink, config.CACert = own, busd, busd.caPEM
		config.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	})
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	// A restore from before the removal: alice's key is not revoked in it.
	busd.restore(original, b.recordOf(bob))
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if len(logged) != 1 || !strings.Contains(logged[0], "new epoch") {
		t.Errorf("logged %q, want the new epoch", logged)
	}
	if !b.refusesKey(bob, alice, aliceAdapter.Fingerprint()) {
		t.Error("the new epoch took alice's revocation back from the trust store")
	}
	exported, err := own.Lookup(ctx, alice)
	if err != nil || !exported.Keys[0].Revoked {
		t.Errorf("alice's record as exported: %+v, %v; want her key still revoked", exported.Keys, err)
	}
}

func TestAnotherIdentityOfTheMachineTakesInWhatOneCommitted(t *testing.T) {
	b := newTestBus(t)
	const carol = "carol@x"
	b.register(alice)
	b.register(bob)
	b.register(carol)
	aliceAdapter, _ := b.open(alice)
	b.remove(alice)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	carolAdapter := b.syncing(carol, busd, own)
	ctx := context.Background()

	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := carolAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if busd.kinds[1] != directory.Unchanged {
		t.Errorf("the second identity got %s, want unchanged: the shared snapshot is current", busd.kinds[1])
	}
	if !b.refusesKey(carol, alice, aliceAdapter.Fingerprint()) {
		t.Error("the second identity did not install the revocation the first committed")
	}
}

func TestOnlyANewerSignedHeadCuesASync(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	if err := bobAdapter.SyncDirectory(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := busd.version
	newer := directory.Version{Epoch: current.Epoch, Revision: current.Revision + 1, StateHash: "next"}
	older := directory.Version{Epoch: current.Epoch, Revision: current.Revision - 1, StateHash: "before"}
	impostor := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	cued := func() bool {
		select {
		case <-bobAdapter.syncWanted:
			return true
		default:
			return false
		}
	}

	for name, head := range map[string][]byte{
		"the current version":  busd.head(current),
		"an older one":         busd.head(older),
		"one of another CA":    impostor.head(newer),
		"one that is unsigned": []byte(`{"payload":"e30=","signature":""}`),
	} {
		bobAdapter.onHead(head)
		if cued() {
			t.Errorf("%s cued a sync", name)
		}
	}
	bobAdapter.onHead(busd.head(newer))
	if !cued() {
		t.Error("a newer head cued no sync")
	}
}

func TestSendToAnAgentEnrolledLaterSyncsTheDirectory(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	b.register(alice)
	busd.publish()
	ctx := context.Background()

	if _, err := bobAdapter.Send(ctx, alice, text("hello")); err != nil {
		t.Fatal(err)
	}
	if len(busd.kinds) != 1 {
		t.Errorf("%d syncs, want 1", len(busd.kinds))
	}
	if _, err := bobAdapter.Send(ctx, alice, text("again")); err != nil || len(busd.kinds) != 1 {
		t.Errorf("a known agent: %v, %d syncs, want no more", err, len(busd.kinds))
	}
	if _, err := bobAdapter.Publish(ctx, "general", text("@"+alice+" hi")); err != nil || len(busd.kinds) != 1 {
		t.Errorf("a post that mentions a known agent: %v, %d syncs, want no more", err, len(busd.kinds))
	}
}

func TestSyncsOnAMissAreRateLimited(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	bobAdapter := b.syncing(bob, busd, trust.DirectoryPath(t.TempDir()))
	ctx := context.Background()

	for _, stranger := range []string{"carol@x", "dave@x", "erin@x"} {
		if _, err := bobAdapter.Send(ctx, stranger, text("hi")); !errors.Is(err, ErrBadTargets) {
			t.Fatalf("send to %s: %v, want ErrBadTargets", stranger, err)
		}
	}
	if len(busd.kinds) != 1 {
		t.Errorf("%d syncs for three misses at once, want 1", len(busd.kinds))
	}
	b.now = b.now.Add(missSyncInterval)
	if _, err := bobAdapter.Publish(ctx, "general", text("@carol@x hi")); !errors.Is(err, ErrBadTargets) || len(busd.kinds) != 2 {
		t.Errorf("a post to a stranger after the interval: %v, %d syncs, want a second one", err, len(busd.kinds))
	}
}

func TestAnUnreachableBusdOnlyLeavesTheAgentUnknown(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	busd.down = errors.New("busd is down")
	own := trust.DirectoryPath(t.TempDir())
	if err := own.Write(b.recordOf(alice)); err != nil {
		t.Fatal(err)
	}
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()

	if _, err := bobAdapter.Send(ctx, alice, text("known")); err != nil {
		t.Errorf("an agent of the local directory could not be written to because busd is down: %v", err)
	}
	_, err := bobAdapter.Send(ctx, "carol@x", text("unknown"))
	if !errors.Is(err, ErrBadTargets) || !strings.Contains(err.Error(), "busd is down") {
		t.Errorf("an unknown agent: %v, want ErrBadTargets that says why the sync failed", err)
	}
}

func TestAMessageFromAnAgentEnrolledLaterIsHandedBackAfterASync(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	aliceAdapter, _ := b.open(alice)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	if err := own.Write(b.recordOf(bob)); err != nil {
		t.Fatal(err)
	}
	bobAdapter, bobTransport := b.open(bob, func(config *Config) {
		config.Directory, config.DirectoryLink, config.CACert = own, busd, busd.caPEM
	})
	if _, err := aliceAdapter.Send(context.Background(), bob, text("hello bob")); err != nil {
		t.Fatal(err)
	}

	poll(t, bobAdapter)
	if senders, _ := bobAdapter.UnknownSenders(); len(senders) != 1 || len(inboxOf(t, bobAdapter)) != 0 {
		t.Fatalf("unknown senders %v: alice's message should wait", senders)
	}
	select {
	case <-bobAdapter.syncWanted:
	default:
		t.Error("the unknown sender asked for no sync")
	}
	if err := bobAdapter.SyncDirectory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bobTransport.released != 1 {
		t.Errorf("%d deliveries handed back, want alice's", bobTransport.released)
	}
	poll(t, bobAdapter)

	if inbox := inboxOf(t, bobAdapter); len(inbox) != 1 || inbox[0].Envelope.From != alice {
		t.Errorf("inbox = %+v, want alice's message", inbox)
	}
	if senders, _ := bobAdapter.UnknownSenders(); len(senders) != 0 {
		t.Errorf("unknown senders %v after the sync", senders)
	}
}

func TestADiffThatDoesNotApplyIsReplacedByTheWholeDirectory(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	b.register(alice)
	busd.publish()
	busd.badDiffs = true

	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if want := []string{directory.Full, directory.Diff, directory.Full}; fmt.Sprint(busd.kinds) != fmt.Sprint(want) {
		t.Errorf("busd answered %v, want %v", busd.kinds, want)
	}
	if _, err := own.Lookup(ctx, alice); err != nil {
		t.Errorf("the full directory did not bring alice: %v", err)
	}
}

func TestAnUnchangedSyncRepairsAnExportCutShort(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	busd := newFakeBusd(t, trust.DirectoryPath(b.configDir))
	own := trust.DirectoryPath(t.TempDir())
	bobAdapter := b.syncing(bob, busd, own)
	ctx := context.Background()
	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	// A crash in the export: alice's lookup file and the note of the finished export are missing.
	for _, name := range []string{bus.AgentToken(alice) + ".json", "snapshot.exported"} {
		if err := os.Remove(filepath.Join(string(own), name)); err != nil {
			t.Fatal(err)
		}
	}

	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if busd.kinds[len(busd.kinds)-1] != directory.Unchanged {
		t.Fatalf("busd answered %v", busd.kinds)
	}
	if _, err := own.Lookup(ctx, alice); err != nil {
		t.Errorf("alice's lookup after an unchanged sync: %v", err)
	}
}

func TestTheMessagesHeldForUnknownSendersAreBoundedAndExpire(t *testing.T) {
	b := newTestBus(t)
	b.register(bob)
	bobAdapter, _ := b.open(bob)
	release := func(context.Context) error { return nil }
	bobAdapter.mu.Lock()
	for sequence := range uint64(maxHeldMessages + 5) {
		bobAdapter.noteUnknownSender("stranger@x", "DM "+bob, bus.Delivery{Record: bus.Record{Sequence: sequence, Payload: []byte("not kept")}, Release: release})
	}
	bobAdapter.mu.Unlock()

	if _, messages := bobAdapter.UnknownSenders(); messages != maxHeldMessages {
		t.Errorf("%d messages remembered, want at most %d", messages, maxHeldMessages)
	}
	b.now = b.now.Add(heldExpiry)
	if senders, messages := bobAdapter.UnknownSenders(); len(senders) != 0 || messages != 0 {
		t.Errorf("after the expiry: %v, %d messages", senders, messages)
	}
}
