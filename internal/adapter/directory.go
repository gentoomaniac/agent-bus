package adapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
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
	// maxDirectoryNotices bounds the notices waiting for the agent; the oldest go first.
	maxDirectoryNotices = 10
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
// lists it in from the next poll on, it is told what changed (TakeDirectoryNotices), and the held
// messages of senders it now has a record of are handed back, so the broker delivers them again at
// once instead of after AckWait. The hand-backs go out after mu is released.
func (adapter *Adapter) took(snapshot directory.Snapshot) {
	adapter.mu.Lock()
	if notice := directoryNotice(adapter.installed, snapshot, adapter.config.AgentID); notice != "" {
		adapter.directoryNotices = append(adapter.directoryNotices, notice)
		if len(adapter.directoryNotices) > maxDirectoryNotices {
			adapter.directoryNotices = adapter.directoryNotices[1:]
		}
	}
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

// TakeDirectoryNotices returns the notices of directory changes the agent was not shown yet, and
// forgets them. They come from this adapter, not from a peer, and ask for no answer.
func (adapter *Adapter) TakeDirectoryNotices() []string {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	notices := adapter.directoryNotices
	adapter.directoryNotices = nil
	return notices
}

// directoryNotice tells the agent what changed from the directory it knew to the one it took in,
// once per version: the net change, so an agent that came and went in between is not mentioned.
// The first directory is summarised rather than announced agent by agent. "" if nothing worth
// telling changed.
func directoryNotice(known, next directory.Snapshot, self string) string {
	if known.Version == next.Version {
		return ""
	}
	if known.Version.IsZero() {
		active := 0
		for _, record := range next.Records {
			if !record.Removed() {
				active++
			}
		}
		return fmt.Sprintf("Directory loaded: %d agents, %d group channels.", active, len(next.Catalogue))
	}
	var news []string
	for _, change := range directory.Changes(known.Projection, next.Projection) {
		switch change.Kind {
		case directory.Joined:
			news = append(news, change.AgentID+" joined")
		case directory.Removed:
			news = append(news, change.AgentID+" was removed")
		case directory.Rekeyed:
			news = append(news, change.AgentID+" has a new key")
		case directory.Channels:
			news = append(news, channelNews(change.Channel, known.Catalogue, next.Catalogue, self))
		}
	}
	notice := ""
	if next.Version.Epoch != known.Version.Epoch {
		notice = "busd's directory was restored (a new epoch); every pinned and revoked key is kept. "
	}
	if len(news) == 0 {
		return strings.TrimSpace(notice)
	}
	return notice + "Directory updated: " + strings.Join(news, "; ") + "."
}

// channelNews is what changed about a group channel, from the agent's point of view.
func channelNews(name string, before, after trust.Catalogue, self string) string {
	readers, existed := before[name]
	now, exists := after[name]
	switch {
	case !existed:
		if slices.Contains(now, self) {
			return "channel " + name + " is new, and you read it"
		}
		return "channel " + name + " is new"
	case !exists:
		return "channel " + name + " is gone"
	case !slices.Contains(readers, self) && slices.Contains(now, self):
		return "you now read channel " + name
	case slices.Contains(readers, self) && !slices.Contains(now, self):
		return "you no longer read channel " + name
	}
	return "the readers of channel " + name + " changed"
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
