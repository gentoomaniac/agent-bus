// Package directory is the published directory of the bus (docs/design/directory-v2.md): the
// projection busd makes of the registry, the catalogue and the readership of the group channels,
// the version busd counts for it, and the net changes between two projections.
package directory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// Version names one published projection. Epoch is the lineage, new only when busd lost or was
// restored from its state; Revision advances only when the content changes; StateHash is the
// projection's Hash. Timestamps play no part: they would make rollback protection depend on clocks.
type Version struct {
	Epoch     string `json:"epoch"`
	Revision  uint64 `json:"revision"`
	StateHash string `json:"state_hash"`
}

// IsZero says no projection was ever taken in.
func (version Version) IsZero() bool { return version == Version{} }

func (version Version) String() string {
	if version.IsZero() {
		return "none"
	}
	return fmt.Sprintf("%s/%d", shortEpoch(version.Epoch), version.Revision)
}

func shortEpoch(epoch string) string { return epoch[:min(len(epoch), 8)] }

// Projection is the directory as busd publishes it: every registry record, tombstones included,
// and the group channels with their readers. Normalise it before hashing or comparing.
type Projection struct {
	Records   []bus.AgentRecord `json:"records"`
	Catalogue trust.Catalogue   `json:"catalogue"`
}

// Normalised is the projection in its one serialisable form: records sorted by agent ID, readers
// sorted, and empty lists and maps present rather than null, so that busd and every agent hash the
// same bytes for the same content.
func (projection Projection) Normalised() Projection {
	records := slices.Clone(projection.Records)
	if records == nil {
		records = []bus.AgentRecord{}
	}
	slices.SortFunc(records, func(a, b bus.AgentRecord) int { return strings.Compare(a.AgentID, b.AgentID) })
	catalogue := trust.Catalogue{}
	for name, readers := range projection.Catalogue {
		sorted := slices.Clone(readers)
		if sorted == nil {
			sorted = []string{}
		}
		slices.Sort(sorted)
		catalogue[name] = sorted
	}
	return Projection{Records: records, Catalogue: catalogue}
}

// Hash is the state hash of the normalised projection: SHA-256 of its JSON. encoding/json writes
// struct fields in order and map keys sorted, so the serialisation is deterministic.
func (projection Projection) Hash() string {
	data, err := json.Marshal(projection.Normalised())
	if err != nil {
		panic(err) // records and string maps always marshal
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Validate refuses what no agent may take in, even signed by the CA: an ID or a channel name that
// is not valid (the IDs name files of the local directory), and an agent listed twice.
func (projection Projection) Validate() error {
	seen := map[string]bool{}
	for _, record := range projection.Records {
		if err := bus.ValidateAgentID(record.AgentID); err != nil {
			return fmt.Errorf("a record of the directory: %w", err)
		}
		if seen[record.AgentID] {
			return fmt.Errorf("the directory lists %s twice", record.AgentID)
		}
		seen[record.AgentID] = true
	}
	return validateCatalogue(projection.Catalogue)
}

func validateCatalogue(catalogue trust.Catalogue) error {
	for name, readers := range catalogue {
		if err := bus.ValidateGroupName(name); err != nil {
			return fmt.Errorf("a channel of the directory: %w", err)
		}
		for _, reader := range readers {
			if err := bus.ValidateAgentID(reader); err != nil {
				return fmt.Errorf("a reader of %s: %w", name, err)
			}
		}
	}
	return nil
}

// byID indexes the records by agent ID.
func (projection Projection) byID() map[string]bus.AgentRecord {
	records := make(map[string]bus.AgentRecord, len(projection.Records))
	for _, record := range projection.Records {
		records[record.AgentID] = record
	}
	return records
}

// Kinds of Change.
const (
	Joined   = "joined"
	Removed  = "removed"
	Rekeyed  = "rekeyed"  // the agent's current key is another one
	Channels = "channels" // a group channel was created or dropped, or its readers changed
)

// Change is one net change between two projections. It names an agent, or for Channels a channel.
type Change struct {
	Kind    string `json:"kind"`
	AgentID string `json:"agent_id,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// Changes are the net changes from before to after, agents first, each sorted. An agent that
// joined and was removed in between is in neither as an active agent, so it is not mentioned.
func Changes(before, after Projection) []Change {
	var changes []Change
	known := before.byID()
	for _, record := range after.Normalised().Records {
		earlier, found := known[record.AgentID]
		switch {
		case !found && !record.Removed():
			changes = append(changes, Change{Kind: Joined, AgentID: record.AgentID})
		case found && !earlier.Removed() && record.Removed():
			changes = append(changes, Change{Kind: Removed, AgentID: record.AgentID})
		case found && !record.Removed() && earlier.Fingerprint != record.Fingerprint:
			changes = append(changes, Change{Kind: Rekeyed, AgentID: record.AgentID})
		}
	}
	old, current := before.Normalised().Catalogue, after.Normalised().Catalogue
	names := slices.Collect(maps.Keys(old))
	for name := range current {
		if _, listed := old[name]; !listed {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		readers, wasListed := old[name]
		now, isListed := current[name]
		if wasListed != isListed || !slices.Equal(readers, now) {
			changes = append(changes, Change{Kind: Channels, Channel: name})
		}
	}
	return changes
}
