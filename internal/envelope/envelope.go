// Package envelope is the signed message of the agent bus (docs/design/protocol.md, docs/design/identity-and-trust.md): its fields, the
// canonical form that is signed, and the checks that need no state (size, version, signature,
// `to` binding, timestamp). Trust in the signing key is the caller's business.
package envelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

const (
	// Version is the only envelope version this code reads and writes.
	Version = 1
	// MaxBodyBytes and MaxRecordBytes are the limits of docs/design/protocol.md: 64 KB of body, 96 KB for the record.
	MaxBodyBytes   = 64 * 1024
	MaxRecordBytes = 96 * 1024
	// MaxClockSkew is how far in the future a timestamp may lie (docs/design/identity-and-trust.md step 5). Old ones are fine.
	MaxClockSkew = 5 * time.Minute
)

// Types are the message types of docs/design/protocol.md.
var Types = []string{"message", "request", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack", "key_rotation", "invite"}

// ResponseBearingTypes expect an answer; the other types close a loop or only inform (docs/design/protocol.md).
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
// channel must carry (docs/design/identity-and-trust.md step 2).
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

// idAlphabet is Crockford base32, the characters of a ULID.
const idAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID: 48 bits of milliseconds and 80 random bits as 26 Crockford base32
// characters, so IDs sort by creation time.
func NewID(now time.Time) string {
	var raw [16]byte
	milliseconds := uint64(now.UnixMilli())
	for i := 0; i < 6; i++ {
		raw[i] = byte(milliseconds >> (8 * (5 - i)))
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		panic(err) // the system's random source is broken
	}
	return FormatID(raw)
}

// FormatID writes the 128 bits of a ULID as its 26 characters.
func FormatID(raw [16]byte) string {
	high, low := binary.BigEndian.Uint64(raw[:8]), binary.BigEndian.Uint64(raw[8:])
	var id [26]byte
	for i := len(id) - 1; i >= 0; i-- {
		id[i] = idAlphabet[low&31]
		low = low>>5 | high<<59
		high >>= 5
	}
	return string(id[:])
}

// ParseID reads the 128 bits of a ULID, for a reader that keeps many IDs and wants 16 bytes each.
// It says false for text that is not a ULID.
func ParseID(id string) ([16]byte, bool) {
	var raw [16]byte
	if !idShape.MatchString(id) {
		return raw, false
	}
	var high, low uint64
	for i := 0; i < len(id); i++ {
		high = high<<5 | low>>59
		low = low<<5 | uint64(strings.IndexByte(idAlphabet, id[i]))
	}
	binary.BigEndian.PutUint64(raw[:8], high)
	binary.BigEndian.PutUint64(raw[8:], low)
	return raw, true
}

// Sign returns the record to publish. It refuses an envelope that Validate would refuse and a
// record over the size limit.
func Sign(env Envelope, key ed25519.PrivateKey) ([]byte, error) {
	if err := env.Validate(); err != nil {
		return nil, err
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

// Parse checks size, version and schema and prepares the signature check. It runs before anything else
// is done with a record (docs/design/identity-and-trust.md steps 0 and 1).
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
	// Field names are matched exactly: the decoder below would also accept "targetſ" for targets.
	if err := checkExactFieldNames(payload); err != nil {
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
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &Signed{Envelope: env, canonical: canonical, signature: signature}, nil
}

// idShape is a ULID: 26 Crockford base32 characters, the first at most 7 (48 bits of time).
var idShape = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// maxThreadBytes bounds a thread name; the adapter's own are ULIDs, an agent may choose its own.
const maxThreadBytes = 128

// Validate checks the schema of a v1 envelope that was read from the wire: every required field
// is present and well formed. A field this version does not know is ignored (it is still signed),
// so a field that receivers must understand needs a new version (docs/design/protocol.md).
func (env Envelope) Validate() error {
	if !idShape.MatchString(env.ID) {
		return fmt.Errorf("id %q is not a ULID", env.ID)
	}
	if env.Thread == "" || len(env.Thread) > maxThreadBytes {
		return fmt.Errorf("thread must be 1 to %d bytes", maxThreadBytes)
	}
	if env.TS.IsZero() {
		return errors.New("ts is missing")
	}
	if err := bus.ValidateAgentID(env.From); err != nil {
		return fmt.Errorf("invalid from: %w", err)
	}
	if (env.To.Kind != ToAgent && env.To.Kind != ToGroup) || env.To.Name == "" {
		return fmt.Errorf("invalid to: kind %q, name %q", env.To.Kind, env.To.Name)
	}
	if !slices.Contains(Types, env.Type) {
		return fmt.Errorf("unknown message type %q", env.Type)
	}
	if env.Hops < 0 {
		return fmt.Errorf("negative hops %d", env.Hops)
	}
	if err := env.CheckTargets(); err != nil {
		return err
	}
	return env.checkBodySize()
}

func (env Envelope) checkBodySize() error {
	body, err := json.Marshal(env.Body)
	if err != nil {
		return err
	}
	if len(body) > MaxBodyBytes {
		return fmt.Errorf("body is %d bytes, over the limit of %d; send a reference instead", len(body), MaxBodyBytes)
	}
	return nil
}

// checkExactFieldNames applies objectWithExactKeys to the record, the envelope, `to` and `body`.
func checkExactFieldNames(payload []byte) error {
	outer, err := objectWithExactKeys(payload, record{})
	if err != nil {
		return err
	}
	fields, err := objectWithExactKeys(outer["envelope"], Envelope{})
	if err != nil {
		return err
	}
	for name, shape := range map[string]any{"to": To{}, "body": Body{}} {
		if raw, present := fields[name]; present && string(raw) != "null" {
			if _, err := objectWithExactKeys(raw, shape); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckTargets accepts agent IDs without duplicates, or the single value "@all". Every message
// is aimed at someone (docs/design/protocol.md), so none is not accepted.
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

// mentionWordChar is a character that continues a word, so "@all" followed by it is another word.
var mentionWordChar = regexp.MustCompile(`[A-Za-z0-9_@-]`)

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
			// "@all-hands", "@all_hands" or "@allX" is not "@all": the word must end there.
			if rest := text[match[3]:]; rest != "" && mentionWordChar.MatchString(rest[:1]) {
				continue
			}
			return []string{TargetAll}
		}
		if !slices.Contains(targets, name) {
			targets = append(targets, name)
		}
	}
	return targets
}

// Digest identifies the signed envelope: a hash of its canonical form, so the same message in
// differently written JSON (whitespace, escapes, key order, unsigned additions to the record) has
// the same digest.
func (signed *Signed) Digest() string {
	sum := sha256.Sum256(signed.canonical)
	return hex.EncodeToString(sum[:])
}

func (signed *Signed) Verify(key ed25519.PublicKey) bool {
	return ed25519.Verify(key, signed.canonical, signed.signature)
}

// CheckTo rejects a record that was consumed from a channel it was not addressed to, so a signed
// message cannot be replayed into another inbox (docs/design/identity-and-trust.md step 2). A DM targets exactly its recipient.
func (env Envelope) CheckTo(channel bus.Channel) error {
	if want := ToFor(channel); env.To != want {
		return fmt.Errorf("addressed to %s %q, but consumed from %s", env.To.Kind, env.To.Name, channel)
	}
	if env.To.Kind == ToAgent && !slices.Equal(env.Targets, []string{env.To.Name}) {
		return fmt.Errorf("a DM to %s must target exactly it, not %v", env.To.Name, env.Targets)
	}
	return nil
}

// CheckNotFuture rejects a timestamp too far ahead of now (docs/design/identity-and-trust.md step 5).
func (env Envelope) CheckNotFuture(now time.Time) error {
	if env.TS.After(now.Add(MaxClockSkew)) {
		return fmt.Errorf("timestamp %s is more than %s in the future", env.TS.Format(time.RFC3339), MaxClockSkew)
	}
	return nil
}
