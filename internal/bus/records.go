package bus

import "time"

// TopicRecord is the catalogue entry of a group channel, keyed by channel name
// (docs/design/agent-channels.md).
type TopicRecord struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Owner is the channel owner: the agent that created the channel through busd. Empty means the
	// bus owner, which owns every channel agent-busctl made, also those of a release before owners.
	Owner     string    `json:"owner,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"` // zero for a channel of a release before owners
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentRecord is the registry entry of an agent, keyed by its agent token. PublicKey and
// Fingerprint always name the current key, the last entry of Keys. A reader treats a record without
// Keys (written before key history existed) as a history of one key: PublicKey and Fingerprint.
// A removed agent keeps its record as a tombstone (RemovedAt set, current key revoked): the ID is
// never registered again and receivers learn the key is revoked (docs/design/access-control.md).
type AgentRecord struct {
	AgentID      string     `json:"agent_id"`
	PublicKey    string     `json:"public_key"` // base64 of the raw 32-byte Ed25519 key
	Fingerprint  string     `json:"fingerprint"`
	RegisteredAt time.Time  `json:"registered_at"`
	Keys         []KeyEntry `json:"keys"`
	RemovedAt    *time.Time `json:"removed_at,omitempty"`
}

// Removed says the agent was removed from the bus.
func (record AgentRecord) Removed() bool { return record.RemovedAt != nil }

// KeyEntry is one key in an agent's key history (docs/design/bus-model.md, docs/design/identity-and-trust.md).
type KeyEntry struct {
	PublicKey   string    `json:"public_key"` // base64 of the raw 32-byte Ed25519 key
	Fingerprint string    `json:"fingerprint"`
	ValidFrom   time.Time `json:"valid_from"`
	// RotationSig is the base64 signature of the previous key over this entry. It is empty for the
	// first key and for a key added after a revocation.
	RotationSig string     `json:"rotation_sig"`
	Revoked     bool       `json:"revoked"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}
