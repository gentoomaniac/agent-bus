package adapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	// missSyncInterval is the least time between two syncs caused by an unknown agent, so that an
	// agent busd does not know either costs one sync, not one per message.
	missSyncInterval = 5 * time.Second
	// syncTimeout bounds one sync: it may run while an agent sends, which must not wait long for a
	// busd that is down.
	syncTimeout = 10 * time.Second
	// commitAttempts is how often a sync starts over when another identity of the machine
	// committed between its read and its write.
	commitAttempts = 3
)

// DirectoryLink is the way to busd's published directory (docs/design/directory-v2.md).
// broker.Transport implements it on the agent's own login.
type DirectoryLink interface {
	RequestDirectory(ctx context.Context, request []byte) ([]byte, error)
	WatchDirectoryHeads(handler func(head []byte)) (stop func(), err error)
}

// snapshots is the store of the local directory, if the adapter syncs one: only a FileDirectory
// has a snapshot, and only an adapter with a DirectoryLink syncs it.
func (adapter *Adapter) snapshots() (directory.Store, bool) {
	dir, isFile := adapter.config.Directory.(trust.FileDirectory)
	return directory.Store{Dir: dir}, isFile && adapter.config.DirectoryLink != nil
}

// SyncDirectory brings the local directory up to date with busd: it asks for what changed since
// the local snapshot, verifies the answer completely, installs every revocation it names into the
// trust store, and only then commits the snapshot. The request goes out with no lock of the
// adapter held. A failure leaves the local directory as it was: what is known keeps working.
func (adapter *Adapter) SyncDirectory(ctx context.Context) error {
	if err := adapter.Ready(); err != nil {
		return err
	}
	store, syncs := adapter.snapshots()
	if !syncs {
		return nil
	}
	adapter.syncMu.Lock()
	defer adapter.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	for range commitAttempts {
		local, err := store.Load()
		if err != nil {
			return err
		}
		request := directory.NewSyncRequest(local.Version)
		answer, err := adapter.config.DirectoryLink.RequestDirectory(ctx, request)
		if err != nil {
			return err
		}
		reply, err := directory.VerifySyncReply(adapter.config.CACert, answer, request)
		if err != nil {
			return err
		}
		projection, err := directory.Apply(local.Version, local.Projection, reply)
		if err != nil {
			return fmt.Errorf("refusing busd's directory: %w", err)
		}
		next := local.Next(reply.Version(), projection)
		if err := adapter.revoke(next.Revoked); err != nil {
			return err
		}
		if reply.Kind != directory.Unchanged {
			next, err = store.Commit(local.Version, reply.Version(), projection)
			if errors.Is(err, directory.ErrMoved) {
				continue
			}
			if err != nil {
				return err
			}
		}
		if !local.Version.IsZero() && reply.Epoch != local.Version.Epoch {
			adapter.config.Log("busd's directory has a new epoch (busd lost or was restored from its state): took in %s over %s; every pin and revoked key is kept", next.Version, local.Version)
			_ = adapter.alert("directory: new epoch %s over %s; pins and revocations kept", next.Version, local.Version)
		}
		adapter.took(next)
		return nil
	}
	return errors.New("the local directory kept changing during the sync; the next one tries again")
}

// revoke installs revocations into the trust store, which refuses those keys from then on.
func (adapter *Adapter) revoke(revoked map[string]string) error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for fingerprint, agentID := range revoked {
		if err := adapter.trust.Revoke(agentID, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

// took makes snapshot the directory this process knows: the agent reads the groups its catalogue
// lists it in from the next poll on, and the held messages of senders it now has a record of are
// handed back, so the broker delivers them again at once instead of after AckWait. The hand-backs
// go out after mu is released.
func (adapter *Adapter) took(snapshot directory.Snapshot) {
	adapter.mu.Lock()
	adapter.installed = snapshot
	adapter.channels = adapter.channelsFor(snapshot.Catalogue)
	var handBacks []func(context.Context) error
	for sender, held := range adapter.unknownSenders {
		if slices.ContainsFunc(snapshot.Records, func(record bus.AgentRecord) bool { return record.AgentID == sender }) {
			for _, release := range held {
				handBacks = append(handBacks, release)
			}
			delete(adapter.unknownSenders, sender)
		}
	}
	adapter.mu.Unlock()
	for _, release := range handBacks {
		_ = release(context.Background()) // a failed hand-back only waits for AckWait
	}
}

// RunDirectory keeps the local directory in sync until ctx ends: it watches busd's heads, syncs at
// once, and then whenever a head names a newer version, something asks for it
// (RequestDirectorySync), and every fallback as a backup. After a reconnect the next head, at most
// a few seconds away, says whether anything changed. Each round also re-reads the local files for
// records copied in by hand. Failures go to report.
func (adapter *Adapter) RunDirectory(ctx context.Context, fallback time.Duration, report func(error)) {
	if _, syncs := adapter.snapshots(); syncs {
		stop, err := adapter.config.DirectoryLink.WatchDirectoryHeads(adapter.onHead)
		if err != nil {
			report(fmt.Errorf("watching busd's directory heads: %w", err))
		} else {
			defer stop()
		}
	}
	ticker := time.NewTicker(fallback)
	defer ticker.Stop()
	for {
		if err := errors.Join(adapter.SyncDirectory(ctx), adapter.RefreshDirectory(ctx)); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-adapter.syncWanted:
		}
	}
}

// RequestDirectorySync makes RunDirectory sync now. Requests while one is due add nothing.
func (adapter *Adapter) RequestDirectorySync() {
	select {
	case adapter.syncWanted <- struct{}{}:
	default:
	}
}

// onHead takes a head busd announced. Only a head signed by the CA counts, and it is only a cue to
// sync: nothing of it is installed. Duplicate and reordered heads change nothing.
func (adapter *Adapter) onHead(message []byte) {
	head, err := directory.VerifyHead(adapter.config.CACert, message)
	if err != nil {
		return
	}
	adapter.mu.Lock()
	have := adapter.installed.Version
	adapter.mu.Unlock()
	switch {
	case head.Version == have:
		return
	case head.Epoch == have.Epoch && head.Revision < have.Revision:
		return // older than what is installed
	case head.Epoch == have.Epoch && head.Revision == have.Revision:
		// The sync asks from this version, which busd does not have, and gets the full directory.
		adapter.config.Log("busd announces directory %s with another state hash than the local one: syncing it in full", head.Version)
	}
	adapter.RequestDirectorySync()
}

// syncOnMiss syncs the directory because an agent is missing from it, at most once per
// missSyncInterval; a call inside the interval waits for the sync that runs and then returns nil.
// It must not be called with mu held.
func (adapter *Adapter) syncOnMiss(ctx context.Context) error {
	if _, syncs := adapter.snapshots(); !syncs {
		return nil
	}
	adapter.missMu.Lock()
	defer adapter.missMu.Unlock()
	if adapter.config.Now().Sub(adapter.lastMissSync) < missSyncInterval {
		return nil
	}
	err := adapter.SyncDirectory(ctx)
	adapter.lastMissSync = adapter.config.Now()
	return err
}

// learnUnknown syncs the directory once if any of the agents is missing from it, and returns the
// sync's error for the refusal to name. "@all" and invalid IDs are left to the checks that follow.
func (adapter *Adapter) learnUnknown(ctx context.Context, agentIDs []string) error {
	for _, agentID := range agentIDs {
		if agentID == envelope.TargetAll || bus.ValidateAgentID(agentID) != nil {
			continue
		}
		if _, err := adapter.config.Directory.Lookup(ctx, agentID); errors.Is(err, fs.ErrNotExist) {
			return adapter.syncOnMiss(ctx)
		}
	}
	return nil
}

// wantSyncForSender asks for a sync because a message's sender has no record, at most once per
// missSyncInterval. The caller holds mu.
func (adapter *Adapter) wantSyncForSender() {
	if now := adapter.config.Now(); now.Sub(adapter.lastSenderSync) >= missSyncInterval {
		adapter.lastSenderSync = now
		adapter.RequestDirectorySync()
	}
}
