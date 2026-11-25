package directory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// The snapshot, its lock and the version its exports were last written for live in the local
// directory. No name ends in .json, so FileDirectory.Records does not take them for agents.
const (
	snapshotFile = "snapshot"
	lockFile     = "snapshot.lock"
	exportedFile = "snapshot.exported"
	// lockWait bounds the wait for another identity of the machine that is committing.
	lockWait = 10 * time.Second
)

// ErrMoved says the snapshot on disk is no longer at the base a commit was made for: another
// identity of the machine committed meanwhile.
var ErrMoved = errors.New("the local directory changed meanwhile")

// Snapshot is the local copy of the published directory: the projection at Version (the
// checkpoint), and every key this machine ever saw revoked, which no later directory takes back.
type Snapshot struct {
	Version Version `json:"version"`
	Projection
	Revoked map[string]string `json:"revoked,omitempty"` // agent ID by key fingerprint
}

// Next is the snapshot after taking in projection at version: the revocations it names are added
// to those already known.
func (snapshot Snapshot) Next(version Version, projection Projection) Snapshot {
	revoked := map[string]string{}
	for fingerprint, agentID := range snapshot.Revoked {
		revoked[fingerprint] = agentID
	}
	for _, record := range projection.Records {
		for _, key := range record.Keys {
			if key.Revoked {
				revoked[key.Fingerprint] = record.AgentID
			}
		}
	}
	return Snapshot{Version: version, Projection: projection, Revoked: revoked}
}

// Store is the snapshot of a local directory (`<config-dir>/directory`). Every identity of the
// machine shares it, so commits take a lock that works across processes.
type Store struct{ Dir trust.FileDirectory }

func (store Store) path(name string) string { return filepath.Join(string(store.Dir), name) }

// Load reads the snapshot; a directory without one has the zero Snapshot. It needs no lock: a
// commit replaces the file in one rename.
func (store Store) Load() (Snapshot, error) {
	var snapshot Snapshot
	data, err := os.ReadFile(store.path(snapshotFile))
	if errors.Is(err, fs.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("reading %s: %w", store.path(snapshotFile), err)
	}
	return snapshot, nil
}

// Commit takes projection at version in, if the snapshot on disk is still at base, and returns
// the snapshot written (Next of the one it replaced). Otherwise it writes nothing and returns
// ErrMoved. The snapshot and its checkpoint are one file, replaced atomically; the per-agent
// files and the catalogue are then exported from it, for the lookups.
func (store Store) Commit(base, version Version, projection Projection) (Snapshot, error) {
	unlock, err := files.Lock(store.path(lockFile), lockWait)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	current, err := store.Load()
	if err != nil {
		return Snapshot{}, err
	}
	if current.Version != base {
		return Snapshot{}, ErrMoved
	}
	next := current.Next(version, projection)
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Snapshot{}, err
	}
	if err := files.Write(store.path(snapshotFile), data, files.PublicMode, true); err != nil {
		return Snapshot{}, err
	}
	return next, store.export(next)
}

// EnsureExported writes the exports again if they were not finished for the snapshot's version: a
// crash or a failed write between a commit and the end of its export would otherwise leave lookups
// on the records of an older directory. It runs under the commit lock, so it sees a snapshot no
// other identity is replacing.
func (store Store) EnsureExported() error {
	unlock, err := files.Lock(store.path(lockFile), lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	snapshot, err := store.Load()
	if err != nil || snapshot.Version.IsZero() {
		return err
	}
	var exported Version
	data, err := os.ReadFile(store.path(exportedFile))
	if err == nil && json.Unmarshal(data, &exported) == nil && exported == snapshot.Version {
		return nil
	}
	return store.export(snapshot)
}

// export writes every record, with every key the machine knows to be revoked marked so, and the
// catalogue, and then notes the version it did so for. A record that is no longer in the snapshot
// keeps its file: absence never means removal, and a record copied in by hand stays usable.
func (store Store) export(snapshot Snapshot) error {
	for _, record := range snapshot.Records {
		if err := store.Dir.Write(withRevocations(record, snapshot.Revoked)); err != nil {
			return err
		}
	}
	if err := store.Dir.WriteCatalogue(snapshot.Catalogue); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot.Version)
	if err != nil {
		return err
	}
	return files.Write(store.path(exportedFile), data, files.PublicMode, true)
}

func withRevocations(record bus.AgentRecord, revoked map[string]string) bus.AgentRecord {
	record.Keys = append([]bus.KeyEntry(nil), record.Keys...)
	for i, key := range record.Keys {
		if revoked[key.Fingerprint] != "" {
			record.Keys[i].Revoked = true
		}
	}
	return record
}
