package bus

import "time"

// TopicRecord is the value of a record in `agents.topics`, keyed by topic name.
type TopicRecord struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentRecord is the value of a record in `agents.registry`, keyed by agent ID. PublicKey and
// Fingerprint always name the current key, the last entry of Keys. A reader treats a record without
// Keys (written before key history existed) as a history of one key: PublicKey and Fingerprint.
type AgentRecord struct {
	AgentID      string     `json:"agent_id"`
	PublicKey    string     `json:"public_key"` // base64 of the raw 32-byte Ed25519 key
	Fingerprint  string     `json:"fingerprint"`
	RegisteredAt time.Time  `json:"registered_at"`
	Keys         []KeyEntry `json:"keys"`
}

// KeyEntry is one key in an agent's key history (design.md §4, §6).
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
