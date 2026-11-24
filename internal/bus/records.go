package bus

import "time"

// TopicRecord is the value of a record in `agents.topics`, keyed by topic name.
type TopicRecord struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentRecord is the value of a record in `agents.registry`, keyed by agent ID.
type AgentRecord struct {
	AgentID      string    `json:"agent_id"`
	PublicKey    string    `json:"public_key"` // base64 of the raw 32-byte Ed25519 key
	Fingerprint  string    `json:"fingerprint"`
	RegisteredAt time.Time `json:"registered_at"`
}
