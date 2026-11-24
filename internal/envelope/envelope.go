// Package envelope is the signed message of the agent bus (design.md §5, §6): its fields, the
// canonical form that is signed, and the checks that need no state (size, version, signature,
// `to` binding, timestamp). Trust in the signing key is the caller's business.
package envelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

const (
	// Version is the only envelope version this code reads and writes.
	Version = 1
	// MaxBodyBytes and MaxRecordBytes are the limits of §5: 64 KB of body, 96 KB for the record.
	MaxBodyBytes   = 64 * 1024
	MaxRecordBytes = 96 * 1024
	// MaxClockSkew is how far in the future a timestamp may lie (§6 step 5). Old ones are fine.
	MaxClockSkew = 5 * time.Minute
)

// Types are the message types of §5.
var Types = []string{"message", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack", "key_rotation", "invite"}

// Destination kinds. `general` is the group named "general".
const (
	ToAgent = "agent"
	ToGroup = "group"
)

type To struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Body struct {
	Title string   `json:"title,omitempty"`
	Text  string   `json:"text,omitempty"`
	Refs  []string `json:"refs,omitempty"`
}

type Envelope struct {
	Version int       `json:"v"`
	ID      string    `json:"id"`
	TS      time.Time `json:"ts"`
	From    string    `json:"from"`
	To      To        `json:"to"`
	Type    string    `json:"type"`
	Thread  string    `json:"thread"`
	ReplyTo *string   `json:"reply_to"`
	Hops    int       `json:"hops"`
	KeyFP   string    `json:"key_fp"`
	Body    Body      `json:"body"`
}

// record is the stored form: the envelope exactly as the sender wrote it, and its signature.
type record struct {
	Envelope json.RawMessage `json:"envelope"`
	Sig      string          `json:"sig"`
}

// Signed is a parsed record whose signature has not been checked yet.
type Signed struct {
	Envelope  Envelope
	canonical []byte
	signature []byte
}

// ToFor is the destination field that belongs to a channel: what a record consumed from that
// channel must carry (§6 step 2).
func ToFor(channel bus.Channel) To {
	switch channel.Kind {
	case bus.DM:
		return To{Kind: ToAgent, Name: channel.Name}
	case bus.Group:
		return To{Kind: ToGroup, Name: channel.Name}
	default:
		return To{Kind: ToGroup, Name: "general"}
	}
}

// NewID returns a ULID: 48 bits of milliseconds and 80 random bits as 26 Crockford base32
// characters, so IDs sort by creation time.
func NewID(now time.Time) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var raw [16]byte
	milliseconds := uint64(now.UnixMilli())
	for i := 0; i < 6; i++ {
		raw[i] = byte(milliseconds >> (8 * (5 - i)))
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		panic(err) // the system's random source is broken
	}
	number := new(big.Int).SetBytes(raw[:])
	var id [26]byte
	for i := len(id) - 1; i >= 0; i-- {
		id[i] = alphabet[number.Uint64()&31]
		number.Rsh(number, 5)
	}
	return string(id[:])
}

// Sign returns the record to publish. It refuses a body or record over the size limits.
func Sign(env Envelope, key ed25519.PrivateKey) ([]byte, error) {
	body, err := json.Marshal(env.Body)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("body is %d bytes, over the limit of %d; send a reference instead", len(body), MaxBodyBytes)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalize(raw)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(record{Envelope: raw, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, canonical))})
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxRecordBytes {
		return nil, fmt.Errorf("message is %d bytes, over the limit of %d", len(payload), MaxRecordBytes)
	}
	return payload, nil
}

// Parse checks size and version and prepares the signature check. It runs before anything else
// is done with a record (§6 steps 0 and 1).
func Parse(payload []byte) (*Signed, error) {
	if len(payload) > MaxRecordBytes {
		return nil, fmt.Errorf("record is %d bytes, over the limit of %d", len(payload), MaxRecordBytes)
	}
	if err := checkUnicode(payload); err != nil {
		return nil, fmt.Errorf("not a record: %w", err)
	}
	// Go's decoder keeps the last of duplicate keys while other readers may keep the first, so a
	// record with a duplicate key could verify one value and show another: refuse it outright.
	if err := checkNoDuplicateKeys(payload); err != nil {
		return nil, fmt.Errorf("not a record: %w", err)
	}
	var stored record
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, fmt.Errorf("not a record: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(stored.Sig)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("malformed signature")
	}
	// The signature covers the envelope as written, so unknown fields count too.
	canonical, err := canonicalize(stored.Envelope)
	if err != nil {
		return nil, fmt.Errorf("envelope cannot be canonicalised: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(stored.Envelope, &env); err != nil {
		return nil, fmt.Errorf("malformed envelope: %w", err)
	}
	if env.Version != Version {
		return nil, fmt.Errorf("unsupported envelope version %d", env.Version)
	}
	return &Signed{Envelope: env, canonical: canonical, signature: signature}, nil
}

func (signed *Signed) Verify(key ed25519.PublicKey) bool {
	return ed25519.Verify(key, signed.canonical, signed.signature)
}

// CheckTo rejects a record that was consumed from a channel it was not addressed to, so a signed
// message cannot be replayed into another inbox (§6 step 2).
func (env Envelope) CheckTo(channel bus.Channel) error {
	if want := ToFor(channel); env.To != want {
		return fmt.Errorf("addressed to %s %q, but consumed from %s", env.To.Kind, env.To.Name, channel)
	}
	return nil
}

// CheckNotFuture rejects a timestamp too far ahead of now (§6 step 5).
func (env Envelope) CheckNotFuture(now time.Time) error {
	if env.TS.After(now.Add(MaxClockSkew)) {
		return fmt.Errorf("timestamp %s is more than %s in the future", env.TS.Format(time.RFC3339), MaxClockSkew)
	}
	return nil
}
