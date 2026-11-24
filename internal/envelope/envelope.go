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
	"regexp"
	"slices"
	"strings"
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
var Types = []string{"message", "request", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack", "key_rotation", "invite"}

// ResponseBearingTypes expect an answer; the other types close a loop or only inform (§5).
var ResponseBearingTypes = []string{"request", "question", "feature_request", "bug_report"}

// Destination kinds. `general` is the group named "general".
const (
	ToAgent = "agent"
	ToGroup = "group"
)

// TargetAll in targets aims a message at everyone who reads the channel.
const TargetAll = "@all"

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
	Targets []string  `json:"targets"`
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

// Sign returns the record to publish. It refuses invalid targets and a body or record over the size limits.
func Sign(env Envelope, key ed25519.PrivateKey) ([]byte, error) {
	if err := env.CheckTargets(); err != nil {
		return nil, err
	}
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
	if err := env.CheckTargets(); err != nil {
		return nil, err
	}
	return &Signed{Envelope: env, canonical: canonical, signature: signature}, nil
}

// CheckTargets accepts agent IDs without duplicates, or the single value "@all". Every message
// is aimed at someone (§5), so none is not accepted.
func (env Envelope) CheckTargets() error {
	if len(env.Targets) == 0 {
		return errors.New("no targets: name a target or @all")
	}
	if slices.Equal(env.Targets, []string{TargetAll}) {
		return nil
	}
	seen := map[string]bool{}
	for _, target := range env.Targets {
		if err := bus.ValidateAgentID(target); err != nil {
			return fmt.Errorf("invalid target: %w", err)
		}
		if seen[target] {
			return fmt.Errorf("target %s is listed twice", target)
		}
		seen[target] = true
	}
	return nil
}

// mention is an `@<agent-id>` or `@all` token that starts the text or follows a space or
// punctuation, so the owner part of an ID such as `lib-x@bob` is not read as a mention.
var mention = regexp.MustCompile(`(?:^|[\s(\[,;:])@([a-z0-9-]+@[a-z0-9-]+|all)`)

// MentionedTargets reads the targets a text names: the agent IDs it mentions in order, or just
// "@all" if it mentions that. It returns nil when there is no mention.
func MentionedTargets(text string) []string {
	var targets []string
	for _, match := range mention.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if name == "all" {
			// "@all-hands" or "@all@x" is not "@all": the name must end there.
			if rest := text[match[3]:]; rest != "" && strings.ContainsAny(rest[:1], "abcdefghijklmnopqrstuvwxyz0123456789-@") {
				continue
			}
			return []string{TargetAll}
		}
		match := []string{"", name}
		if !slices.Contains(targets, match[1]) {
			targets = append(targets, match[1])
		}
	}
	return targets
}

func (signed *Signed) Verify(key ed25519.PublicKey) bool {
	return ed25519.Verify(key, signed.canonical, signed.signature)
}

// CheckTo rejects a record that was consumed from a channel it was not addressed to, so a signed
// message cannot be replayed into another inbox (§6 step 2). A DM targets exactly its recipient.
func (env Envelope) CheckTo(channel bus.Channel) error {
	if want := ToFor(channel); env.To != want {
		return fmt.Errorf("addressed to %s %q, but consumed from %s", env.To.Kind, env.To.Name, channel)
	}
	if env.To.Kind == ToAgent && !slices.Equal(env.Targets, []string{env.To.Name}) {
		return fmt.Errorf("a DM to %s must target exactly it, not %v", env.To.Name, env.Targets)
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
