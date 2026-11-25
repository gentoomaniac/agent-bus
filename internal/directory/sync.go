package directory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// SubjectSync starts the subjects of busd's sync endpoint in the BUS account: each agent publishes
// its requests on SyncSubject of its own token, the only one its grants allow, and busd answers in
// that agent's inbox space.
const SubjectSync = "busd.directory.sync"

// SyncSubject is the subject the agent with this token asks busd for the directory on.
func SyncSubject(agentToken string) string { return SubjectSync + "." + agentToken }

// MaxSyncRequestBytes bounds what busd reads of a request; a real one is about 200 bytes.
const MaxSyncRequestBytes = 1 << 10

// syncDomain is prefixed to what the CA key signs for a sync reply (headDomain for a head), so
// that a signature made for one kind of message, or a certificate, never passes for another.
const syncDomain = "agent-bus/directory-sync/v1\n"

// Signed carries a payload the CA key signed: Payload is exactly the bytes Signature covers, after
// the domain prefix of its kind. Error is set instead when busd answers without signing (it is too
// busy to sign); it is never trusted, only shown.
type Signed struct {
	Payload   []byte `json:"payload,omitempty"`
	Signature []byte `json:"signature,omitempty"`
	Error     string `json:"error,omitempty"`
}

func sign(ca *tlsinit.CA, domain string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	signature, err := ca.SignData(append([]byte(domain), data...))
	if err != nil {
		return nil, err
	}
	return json.Marshal(Signed{Payload: data, Signature: signature})
}

// open checks the CA's signature over a Signed message and only then decodes its payload, which
// must have no field this release does not know.
func open(caPEM []byte, domain string, message []byte, payload any) error {
	var signed Signed
	if err := json.Unmarshal(message, &signed); err != nil {
		return fmt.Errorf("busd's answer is not a signed message: %w", err)
	}
	if len(signed.Payload) == 0 || len(signed.Signature) == 0 {
		if signed.Error != "" {
			return fmt.Errorf("busd: %s", signed.Error)
		}
		return errors.New("busd's answer is not signed")
	}
	if err := tlsinit.CheckDataSignature(caPEM, append([]byte(domain), signed.Payload...), signed.Signature); err != nil {
		return fmt.Errorf("busd's answer is not signed by the CA: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(signed.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return fmt.Errorf("busd's signed answer is malformed: %w", err)
	}
	return nil
}

// Unsigned is busd's answer when it does not sign one, such as when it is too busy.
func Unsigned(reason string) []byte {
	data, _ := json.Marshal(Signed{Error: reason}) // a string always marshals
	return data
}

// SyncRequest asks busd for what changed since the version the agent has. The challenge makes
// every request, and so the request hash a reply is bound to, unique.
type SyncRequest struct {
	Challenge string  `json:"challenge"`
	Since     Version `json:"since"`
}

// NewSyncRequest is the request body for an agent that has since (the zero Version if nothing).
func NewSyncRequest(since Version) []byte {
	challenge := make([]byte, 16)
	if _, err := rand.Read(challenge); err != nil {
		panic(err) // the system's random source is broken
	}
	data, _ := json.Marshal(SyncRequest{Challenge: base64.RawURLEncoding.EncodeToString(challenge), Since: since})
	return data
}

// ParseSyncRequest reads a request as busd gets it, from anyone in BUS.
func ParseSyncRequest(data []byte) (SyncRequest, error) {
	var request SyncRequest
	if len(data) > MaxSyncRequestBytes {
		return request, errors.New("the sync request is too long")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("not a sync request: %w", err)
	}
	if request.Challenge == "" {
		return request, errors.New("the sync request has no challenge")
	}
	return request, nil
}

// RequestHash is what binds a reply to the exact request bytes it answers.
func RequestHash(request []byte) string {
	sum := sha256.Sum256(request)
	return hex.EncodeToString(sum[:])
}

// Kinds of SyncReply.
const (
	Unchanged = "unchanged" // the agent's version is current
	Diff      = "diff"      // the changes from the agent's version
	Full      = "full"      // the whole directory
	Failed    = "error"     // busd cannot answer; Error says why
)

// SyncReply is busd's signed answer. A diff carries the complete current record of every agent
// that changed between From and To, and the whole catalogue if it changed (absent: unchanged;
// present and empty: empty). A full reply carries every record and the catalogue.
type SyncReply struct {
	RequestHash string            `json:"request_hash"`
	Kind        string            `json:"kind"`
	Epoch       string            `json:"epoch,omitempty"`
	From        uint64            `json:"from,omitempty"`
	BaseHash    string            `json:"base_hash,omitempty"`
	To          uint64            `json:"to,omitempty"`
	StateHash   string            `json:"state_hash,omitempty"`
	Records     []bus.AgentRecord `json:"records,omitempty"`
	Catalogue   *trust.Catalogue  `json:"catalogue,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// Version is the version the reply brings the agent to.
func (reply SyncReply) Version() Version {
	return Version{Epoch: reply.Epoch, Revision: reply.To, StateHash: reply.StateHash}
}

// SignSyncReply is the reply as busd sends it.
func SignSyncReply(ca *tlsinit.CA, reply SyncReply) ([]byte, error) {
	return sign(ca, syncDomain, reply)
}

// VerifySyncReply reads busd's answer to request: signed by the CA in caPEM and bound to exactly
// this request, so no recorded reply can be played back. A signed error is returned as an error.
func VerifySyncReply(caPEM, answer, request []byte) (SyncReply, error) {
	var reply SyncReply
	if err := open(caPEM, syncDomain, answer, &reply); err != nil {
		return SyncReply{}, err
	}
	if reply.RequestHash != RequestHash(request) {
		return SyncReply{}, errors.New("busd's signed answer is for another request: a replay")
	}
	if reply.Kind == Failed {
		return SyncReply{}, fmt.Errorf("busd cannot give the directory: %s", reply.Error)
	}
	return reply, nil
}

// Answer is busd's reply to an agent at since, when the current projection is current and base
// is the projection the agent has, if busd still has it.
func Answer(since Version, current Version, projection Projection, base *Projection) SyncReply {
	projection = projection.Normalised()
	reply := SyncReply{Epoch: current.Epoch, To: current.Revision, StateHash: current.StateHash}
	switch {
	case since == current:
		reply.Kind = Unchanged
	case base != nil && since.Epoch == current.Epoch && since.Revision < current.Revision:
		reply.Kind, reply.From, reply.BaseHash = Diff, since.Revision, since.StateHash
		reply.Records, reply.Catalogue = changed(base.Normalised(), projection)
	default:
		reply.Kind, reply.Records, reply.Catalogue = Full, projection.Records, &projection.Catalogue
	}
	return reply
}

// changed is the coalesced change from base to current, both normalised: the records that differ
// and the catalogue if it differs. Removals are tombstone records, so nothing is ever only absent.
func changed(base, current Projection) ([]bus.AgentRecord, *trust.Catalogue) {
	known := base.byID()
	var records []bus.AgentRecord
	for _, record := range current.Records {
		if earlier, found := known[record.AgentID]; !found || !sameRecord(earlier, record) {
			records = append(records, record)
		}
	}
	if maps.EqualFunc(base.Catalogue, current.Catalogue, slices.Equal) {
		return records, nil
	}
	return records, &current.Catalogue
}

// sameRecord compares records as they travel: by their JSON.
func sameRecord(a, b bus.AgentRecord) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return bytes.Equal(first, second)
}

// Apply is the projection a verified reply makes of the agent's own, at version have. It refuses a
// reply that does not fit: a diff onto another base, a revision lower than the agent's in the same
// epoch, an unchanged that is not the agent's version, invalid records, or a result whose hash is
// not the one busd signed. A new epoch is only accepted as a full directory.
func Apply(have Version, projection Projection, reply SyncReply) (Projection, error) {
	sameEpoch := !have.IsZero() && reply.Epoch == have.Epoch
	var next Projection
	switch reply.Kind {
	case Unchanged:
		if reply.Version() != have {
			return Projection{}, fmt.Errorf("busd says %s is unchanged, but this directory is at %s", reply.Version(), have)
		}
		return projection, nil
	case Diff:
		if !sameEpoch || reply.From != have.Revision || reply.BaseHash != have.StateHash {
			return Projection{}, fmt.Errorf("a diff from %s/%d onto a directory at %s", shortEpoch(reply.Epoch), reply.From, have)
		}
		if reply.To <= reply.From {
			return Projection{}, fmt.Errorf("a diff from revision %d back to %d", reply.From, reply.To)
		}
		next = withChanges(projection, reply)
	case Full:
		if sameEpoch && reply.To < have.Revision {
			return Projection{}, fmt.Errorf("busd offers revision %d of a directory at %s: a rollback", reply.To, have)
		}
		if reply.Catalogue == nil {
			return Projection{}, errors.New("a full directory without a catalogue")
		}
		next = Projection{Records: reply.Records, Catalogue: *reply.Catalogue}
	default:
		return Projection{}, fmt.Errorf("an answer of unknown kind %q", reply.Kind)
	}
	next = next.Normalised()
	if err := next.Validate(); err != nil {
		return Projection{}, err
	}
	if hash := next.Hash(); hash != reply.StateHash {
		return Projection{}, fmt.Errorf("the directory busd sent hashes to %s, not to the %s it signed", hash, reply.StateHash)
	}
	return next, nil
}

func withChanges(base Projection, reply SyncReply) Projection {
	records := base.byID()
	for _, record := range reply.Records {
		records[record.AgentID] = record
	}
	next := Projection{Records: slices.Collect(maps.Values(records)), Catalogue: base.Catalogue}
	if reply.Catalogue != nil {
		next.Catalogue = *reply.Catalogue
	}
	return next
}
