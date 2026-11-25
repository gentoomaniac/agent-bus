package enroll

import (
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// Status is where a request stands.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	// StatusBusy refuses for now (a rate limit, a full queue, a registry that cannot be read);
	// the client may ask again.
	StatusBusy Status = "busy"
	// StatusUnknown is the answer for a request busd has no record of (expired, or lost with a state
	// file): the client sends it again.
	StatusUnknown Status = "unknown"
)

// Reply answers a request and a status query. Only StatusApproved carries certificate material, all of
// it public.
type Reply struct {
	Status    Status    `json:"status"`
	RequestID string    `json:"request_id,omitempty"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"` // the reason of a rejection, or why a request was refused
	ExpiresAt time.Time `json:"expires_at,omitzero"`

	Certificate string     `json:"certificate,omitempty"` // PEM
	CA          string     `json:"ca,omitempty"`          // PEM; the client compares it with the CA it trusts
	Directory   *Directory `json:"directory,omitempty"`
}

// StatusQuery asks for the state of one request.
type StatusQuery struct {
	RequestID string `json:"request_id"`
}

// Directory is the public snapshot an enrolled agent starts with: the registry records and the
// channel catalogue (docs/agent-busctl.md, directory export).
type Directory struct {
	Records  []bus.AgentRecord   `json:"records"`
	Channels map[string][]string `json:"channels"`
}

// What the registry says about the ID of a pending request.
const (
	IdentityNew      = "new"      // not registered: approval registers it
	IdentitySameKey  = "same-key" // registered with this signing key: approval only switches it to a certificate
	IdentityReplaces = "replaces" // registered with another key: approval revokes that key and needs a confirmation
)

// Pending is a request as the owner sees it.
type Pending struct {
	RequestID          string    `json:"request_id"`
	Code               string    `json:"code"`
	AgentID            string    `json:"agent_id"`
	Hostname           string    `json:"hostname"`
	SigningFingerprint string    `json:"signing_fingerprint"`
	TLSFingerprint     string    `json:"tls_fingerprint"`
	SubmittedAt        time.Time `json:"submitted_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	Identity           string    `json:"identity"`
}

// ListReply is busd's answer to SubjectList.
type ListReply struct {
	Pending []Pending `json:"pending"`
	Error   string    `json:"error,omitempty"`
}

// SignCommand approves a pending request, named by request ID, approval code or agent ID. Replace
// confirms that an existing identity with another key may be replaced.
type SignCommand struct {
	Ref     string `json:"ref"`
	Replace bool   `json:"replace"`
}

// RejectCommand rejects a pending request.
type RejectCommand struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// CommandReply answers SubjectSign and SubjectReject. Error is empty on success.
type CommandReply struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}
