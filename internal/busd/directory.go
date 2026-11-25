package busd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	directoryStateFile = "directory.json" // in enroll/, next to the enrollment state
	// directoryHistory is how many earlier projections are kept in memory to answer a sync with a
	// diff. An agent further behind, or one asking after a restart of busd, gets the full directory.
	directoryHistory = 16
	// syncInterval and syncBurst bound the signatures busd makes for sync requests, which anyone in
	// BUS may send: agents sync on a change all at once, then about once a minute.
	syncInterval = 20 * time.Millisecond
	syncBurst    = 50
)

// DirectorySource is what the published directory is made of: the registry, the catalogue and the
// read cursors of the group channels. *broker.Admin implements it.
type DirectorySource interface {
	trust.CatalogueSource
	Agents(ctx context.Context) ([]bus.AgentRecord, error)
}

// DirectoryConfig is what a Directory is made of.
type DirectoryConfig struct {
	Source    DirectorySource
	CA        *tlsinit.CA // signs what busd publishes, so that agents verify it against ca.pem alone
	ConfigDir string      // the state is enroll/directory.json in it
	// LockMutations takes the lock that approvals and agent-busctl --direct hold while they change
	// the registry, the cursors and the accounts, so that a projection never shows half an
	// enrollment. Nil: no lock, for tests.
	LockMutations func() (unlock func(), err error)
}

// revision is one projection with its version.
type revision struct {
	Version    directory.Version    `json:"version"`
	Projection directory.Projection `json:"projection"`
}

// Directory is the projection busd publishes (docs/design/directory-v2.md): it is rebuilt from the
// broker, and each change of its content is a new revision, persisted before anyone hears of it.
// busd is its only writer.
type Directory struct {
	config  DirectoryConfig
	limiter *rate.Limiter

	mu      sync.Mutex
	current revision
	earlier []revision // oldest first, at most directoryHistory
}

// NewDirectory reads the projection and version a former run left. State that cannot be read
// counts as lost: the next projection starts a new epoch, which agents accept as a restore.
func NewDirectory(config DirectoryConfig) *Directory {
	published := &Directory{config: config, limiter: rate.NewLimiter(rate.Every(syncInterval), syncBurst)}
	path := published.path()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return published
	}
	if err == nil {
		err = json.Unmarshal(data, &published.current)
	}
	if err == nil && published.current.Projection.Hash() != published.current.Version.StateHash {
		err = errors.New("its content does not match its state hash")
	}
	if err != nil {
		log.Printf("busd: the directory state %s is unusable, a new epoch starts: %v", path, err)
		published.current = revision{}
	}
	return published
}

func (published *Directory) path() string {
	return filepath.Join(published.config.ConfigDir, enrollDir, directoryStateFile)
}

// Version is the version of the current projection; the zero Version before the first one.
func (published *Directory) Version() directory.Version {
	published.mu.Lock()
	defer published.mu.Unlock()
	return published.current.Version
}

// Reconcile builds the projection from the broker and, if its content changed, commits it as the
// next revision. It returns the net changes, nil when there were none.
func (published *Directory) Reconcile(ctx context.Context) ([]directory.Change, error) {
	projection, err := published.build(ctx)
	if err != nil {
		return nil, err
	}
	published.mu.Lock()
	defer published.mu.Unlock()
	hash := projection.Hash()
	previous := published.current
	if hash == previous.Version.StateHash {
		return nil, nil
	}
	next := revision{Version: directory.Version{Epoch: previous.Version.Epoch, Revision: previous.Version.Revision + 1, StateHash: hash}, Projection: projection}
	if next.Version.Epoch == "" {
		next.Version.Epoch = newEpoch()
	}
	// Persisted first: a revision announced and then lost in a crash would be reused for other content.
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := files.Write(published.path(), data, files.PublicMode, true); err != nil {
		return nil, fmt.Errorf("saving the directory: %w", err)
	}
	if !previous.Version.IsZero() {
		published.earlier = append(published.earlier, previous)
		if len(published.earlier) > directoryHistory {
			published.earlier = published.earlier[1:]
		}
	}
	published.current = next
	changes := directory.Changes(previous.Projection, projection)
	log.Printf("busd: directory %s: %d changes", next.Version, len(changes))
	return changes, nil
}

// HandleSync answers a sync request from anyone in BUS. It proves nothing about the asker and
// holds public data only. The answer comes from the committed projection, signed and bound to the
// request; past the rate limit busd answers unsigned, without spending a signature.
func (published *Directory) HandleSync(request []byte) []byte {
	if !published.limiter.Allow() {
		return directory.Unsigned("too many directory requests, try again in a moment")
	}
	reply := directory.SyncReply{RequestHash: directory.RequestHash(request)}
	if parsed, err := directory.ParseSyncRequest(request); err != nil {
		reply.Kind, reply.Error = directory.Failed, err.Error()
	} else {
		reply = published.answer(parsed.Since, reply.RequestHash)
	}
	signed, err := directory.SignSyncReply(published.config.CA, reply)
	if err != nil {
		log.Printf("busd: signing a directory reply: %v", err)
		return directory.Unsigned("busd cannot sign the directory")
	}
	return signed
}

func (published *Directory) answer(since directory.Version, requestHash string) directory.SyncReply {
	published.mu.Lock()
	defer published.mu.Unlock()
	current := published.current
	if current.Version.IsZero() {
		return directory.SyncReply{RequestHash: requestHash, Kind: directory.Failed, Error: "busd has not built the directory yet"}
	}
	var base *directory.Projection
	for _, earlier := range published.earlier {
		if earlier.Version == since {
			base = &earlier.Projection
		}
	}
	reply := directory.Answer(since, current.Version, current.Projection, base)
	reply.RequestHash = requestHash
	return reply
}

// build reads a coherent cut: under the mutation lock, nothing changes the registry or the cursors
// while it is read.
func (published *Directory) build(ctx context.Context) (directory.Projection, error) {
	if published.config.LockMutations != nil {
		unlock, err := published.config.LockMutations()
		if err != nil {
			return directory.Projection{}, err
		}
		defer unlock()
	}
	records, err := published.config.Source.Agents(ctx)
	if err != nil {
		return directory.Projection{}, err
	}
	catalogue, err := trust.BuildCatalogue(ctx, published.config.Source)
	if err != nil {
		return directory.Projection{}, err
	}
	return directory.Projection{Records: records, Catalogue: catalogue}.Normalised(), nil
}

func newEpoch() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // the system's random source is broken
	}
	return hex.EncodeToString(raw)
}
