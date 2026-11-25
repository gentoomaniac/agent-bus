package busd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// enroll makes an agent whose enrollment is complete: its record, its read cursors and its account.
func (f *fixture) enroll(agentID string, key ed25519.PrivateKey) {
	f.register(agentID, key)
	f.registry.cursors = append(f.registry.cursors, agentID+" DM "+agentID+" after 0", agentID+" general after 0")
	f.registry.accounts = append(f.registry.accounts, agentID)
}

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
	reply, err := directory.VerifySyncReply(mustRead(f.t, filepath.Join(f.dir, "ca.pem")), published.HandleSync("bob@x", request), request)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply
}

func TestTheRevisionAdvancesOnlyWhenTheContentChanges(t *testing.T) {
	f := newFixture(t)
	f.enroll("alice@x", f.newKey())
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

	f.enroll("bob@x", f.newKey())
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
	f.enroll("alice@x", f.newKey())
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
	f.enroll("alice@x", f.newKey())
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
	f.enroll("alice@x", f.newKey())
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

	f.enroll("bob@x", f.newKey())
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

	if _, err := directory.VerifySyncReply(caPEM, published.HandleSync("bob@x", request), request); err == nil || !strings.Contains(err.Error(), "not built") {
		t.Errorf("a sync before the first projection: %v", err)
	}
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("not a request")
	if _, err := directory.VerifySyncReply(caPEM, published.HandleSync("bob@x", garbage), garbage); err == nil || !strings.Contains(err.Error(), "not a sync request") {
		t.Errorf("a malformed request: %v", err)
	}
}

func TestSyncsPastTheLimitAreAnsweredWithoutASignature(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	published.limits.all = rate.NewLimiter(rate.Every(time.Hour), 1)
	f.syncFrom(published, directory.Version{})

	request := directory.NewSyncRequest(directory.Version{})
	_, err := directory.VerifySyncReply(mustRead(t, filepath.Join(f.dir, "ca.pem")), published.HandleSync("bob@x", request), request)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("a sync past the limit: %v", err)
	}
}

func TestTheHeadAnnouncesTheCurrentVersionWithTheLastChanges(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	if published.Head() != nil {
		t.Error("a head before the first projection")
	}
	caPEM := mustRead(t, filepath.Join(f.dir, "ca.pem"))
	ctx := context.Background()
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	f.enroll("alice@x", f.newKey())
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	head, err := directory.VerifyHead(caPEM, published.Head())
	if err != nil || head.Version != published.Version() || len(head.Changes) != 1 || head.Changes[0].AgentID != "alice@x" {
		t.Errorf("head = %+v, %v; want version %+v with alice joined", head, err, published.Version())
	}
	restarted, err := directory.VerifyHead(caPEM, f.directory().Head())
	if err != nil || restarted.Version != published.Version() || len(restarted.Changes) != 0 {
		t.Errorf("after a restart the head is %+v, %v; want the same version without hints", restarted, err)
	}
}

func TestARevisionThatCannotBeSavedIsNotAnnounced(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	ctx := context.Background()
	if _, err := published.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	before, head := published.Version(), published.Head()
	f.enroll("alice@x", f.newKey())
	// A file stands where the state's directory was: nothing can be written there.
	if err := os.RemoveAll(filepath.Join(f.dir, enrollDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, enrollDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := published.Reconcile(ctx); err == nil {
		t.Fatal("a revision that could not be saved was taken")
	}
	if published.Version() != before || string(published.Head()) != string(head) {
		t.Errorf("the version moved to %+v without being saved", published.Version())
	}
}

func TestAnEnrollmentIsPublishedOnlyOnceItIsComplete(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	ctx := context.Background()
	f.register("half@x", f.newKey()) // the record of an approval that failed after writing it
	removedAt := f.now
	f.register("gone@x", f.newKey())
	tombstone := f.registry.records["gone@x"]
	tombstone.RemovedAt = &removedAt
	f.registry.records["gone@x"] = tombstone // removed: no cursors, no account
	listed := func() []string {
		t.Helper()
		if _, err := published.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		reply := f.syncFrom(published, directory.Version{})
		var ids []string
		for _, record := range reply.Records {
			ids = append(ids, record.AgentID)
		}
		return ids
	}

	if ids := listed(); !slices.Equal(ids, []string{"gone@x"}) {
		t.Errorf("published %v, want only the tombstone", ids)
	}
	f.registry.cursors = append(f.registry.cursors, "half@x DM half@x after 0", "half@x general after 0")
	if ids := listed(); !slices.Equal(ids, []string{"gone@x"}) {
		t.Errorf("with cursors but no account: published %v", ids)
	}
	f.registry.accounts = append(f.registry.accounts, "half@x")
	if ids := listed(); !slices.Equal(ids, []string{"gone@x", "half@x"}) {
		t.Errorf("once complete: published %v", ids)
	}
	// Made of what the broker holds, so a restart of busd changes nothing.
	if reply := f.syncFrom(f.directory(), directory.Version{}); len(reply.Records) != 2 {
		t.Errorf("after a restart: %d records", len(reply.Records))
	}
}

func TestARepeatedApprovalStartsTheCursorsWhereTheFirstAttemptDid(t *testing.T) {
	f := newFixture(t)
	f.registry.last = 42
	reply := f.submit("new@x", f.newKey(), "")
	f.registry.cursorErr = errors.New("jetstream unreachable")
	if result := f.sign(reply.Code, false); result.Error == "" {
		t.Fatal("the approval did not fail")
	}
	f.registry.last = 50 // messages arrived meanwhile
	f.restart()          // and busd restarted

	if result := f.sign(reply.Code, false); result.Error != "" {
		t.Fatal(result.Error)
	}

	if want := []string{"new@x DM new@x after 42", "new@x general after 42"}; !slices.Equal(f.registry.cursors, want) {
		t.Errorf("cursors %v, want %v", f.registry.cursors, want)
	}
}

func TestEachAgentIsLimitedOnItsOwnBeneathBusdsLimit(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	caPEM := mustRead(t, filepath.Join(f.dir, "ca.pem"))
	answered := func(caller string) bool {
		request := directory.NewSyncRequest(directory.Version{})
		_, err := directory.VerifySyncReply(caPEM, published.HandleSync(caller, request), request)
		return err == nil
	}

	for range callerSyncBurst {
		if !answered("greedy@x") {
			t.Fatal("a sync within the agent's burst was refused")
		}
	}
	if answered("greedy@x") {
		t.Error("an agent past its own limit was answered")
	}
	if !answered("modest@x") {
		t.Error("another agent was refused because of the first one")
	}

	published.limits.all = rate.NewLimiter(rate.Every(time.Hour), 0)
	if answered("fresh@x") {
		t.Error("an agent within its own limit was answered past busd's")
	}
}

func TestTheCursorsStartIsOnDiskBeforeTheFirstCursorExists(t *testing.T) {
	f := newFixture(t)
	f.registry.last = 42
	reply := f.submit("new@x", f.newKey(), "")
	var onDisk []string
	f.registry.onCursor = func() {
		data, _ := os.ReadFile(filepath.Join(f.dir, enrollDir, stateFile))
		onDisk = append(onDisk, string(data))
	}

	if result := f.sign(reply.Code, false); result.Error != "" {
		t.Fatal(result.Error)
	}

	if len(onDisk) != 2 || !strings.Contains(onDisk[0], `"cursors_after":42`) {
		t.Errorf("when the first cursor was made, the state on disk was %q", onDisk)
	}
}

func TestAnApprovalWhoseCursorStartCannotBeSavedChangesNothing(t *testing.T) {
	f := newFixture(t)
	reply := f.submit("new@x", f.newKey(), "")
	if err := os.RemoveAll(filepath.Join(f.dir, enrollDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, enrollDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if result := f.sign(reply.Code, false); !strings.Contains(result.Error, "saving where the read cursors start") {
		t.Errorf("sign = %q, want the save's error", result.Error)
	}
	if len(f.registry.cursors) != 0 || len(f.registry.records) != 0 || len(f.registry.accounts) != 0 {
		t.Errorf("the broker changed: %v, %v, %v", f.registry.cursors, f.registry.records, f.registry.accounts)
	}
}

func TestAReconcileReadsTheAccountsOnce(t *testing.T) {
	f := newFixture(t)
	for _, agentID := range []string{"a@x", "b@x", "c@x"} {
		f.enroll(agentID, f.newKey())
	}

	if _, err := f.directory().Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if f.registry.accountReads != 1 {
		t.Errorf("the accounts were read %d times for three agents, want once", f.registry.accountReads)
	}
}

func TestAnApprovalIsPublishedOnlyOnceItsAccountTookEffect(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	ctx := context.Background()
	publishedKey := func(agentID string) string {
		t.Helper()
		if _, err := published.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		for _, record := range f.syncFrom(published, directory.Version{}).Records {
			if record.AgentID == agentID {
				return record.PublicKey
			}
		}
		return ""
	}

	// A new agent whose account's reload did not take: record and cursors are there.
	first := f.submit("new@x", f.newKey(), "")
	f.registry.accountErr = errors.New("the server did not confirm the reload")
	if result := f.sign(first.Code, false); result.Error == "" {
		t.Fatal("the approval did not fail")
	}
	if key := publishedKey("new@x"); key != "" {
		t.Error("an agent whose account did not take effect was published")
	}
	if result := f.sign(first.Code, false); result.Error != "" {
		t.Fatal(result.Error)
	}
	oldKey := publishedKey("new@x")
	if oldKey == "" {
		t.Fatal("the finished approval was not published")
	}

	// A replacement whose account switch did not take: the account is there, with the old login.
	replacement := f.submit("new@x", f.newKey(), "")
	f.registry.accountErr = errors.New("the server did not confirm the reload")
	if result := f.sign(replacement.Code, true); result.Error == "" {
		t.Fatal("the replacement did not fail")
	}
	if key := publishedKey("new@x"); key != "" {
		t.Errorf("the unfinished replacement was published with key %q", key)
	}
	if result := f.sign(replacement.Code, true); result.Error != "" {
		t.Fatal(result.Error)
	}
	if key := publishedKey("new@x"); key == "" || key == oldKey {
		t.Errorf("after the replacement finished, the published key is %q", key)
	}
	// Made of the state file and the broker: a restart of busd sees the same.
	if reply := f.syncFrom(f.directory(), directory.Version{}); len(reply.Records) != 1 {
		t.Errorf("after a restart: %d records", len(reply.Records))
	}
}

func TestARejectionAfterAFailedApprovalShowsTheAgentAgain(t *testing.T) {
	f := newFixture(t)
	published := f.directory()
	ctx := context.Background()
	key := f.newKey()
	f.enroll("old@x", key)
	listed := func() bool {
		t.Helper()
		if _, err := published.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		return len(f.syncFrom(published, directory.Version{}).Records) == 1
	}
	if !listed() {
		t.Fatal("the enrolled agent is not published")
	}

	// It enrolls again with its own key, and the account switch does not take.
	again := f.submit("old@x", key, "")
	f.registry.accountErr = errors.New("the server did not confirm the reload")
	if result := f.sign(again.Code, false); result.Error == "" {
		t.Fatal("the approval did not fail")
	}
	if listed() {
		t.Error("the agent was published while its approval was half done")
	}

	if result := f.service.Reject(enroll.RejectCommand{Ref: again.Code}); result.Error != "" {
		t.Fatal(result.Error)
	}
	if !listed() {
		t.Error("after the rejection the agent is still left out")
	}
}

func TestAnAutosignedApprovalIsSavedBeforeItIsAnnounced(t *testing.T) {
	f := newFixture(t)
	f.service.limiter = rate.NewLimiter(rate.Inf, 1)
	if err := os.MkdirAll(filepath.Join(f.dir, enrollDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, enrollDir, "autosign"), []byte("sesame\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var unfinishedWhenTold []string
	f.service.config.DirectoryChanged = func() {
		var err error
		if unfinishedWhenTold, err = unfinishedApprovals(f.dir); err != nil {
			t.Error(err)
		}
	}

	if reply := f.submit("auto@x", f.newKey(), "sesame"); reply.Status != enroll.StatusApproved {
		t.Fatalf("autosign: %+v", reply)
	}
	if len(unfinishedWhenTold) != 0 {
		t.Errorf("when the directory was told, the state on disk still had %v approving", unfinishedWhenTold)
	}
}
