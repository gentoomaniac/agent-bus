// Package trust decides which public key may speak for an agent ID (design.md §6): the pinned
// keys of the `known_agents` file, trust on first use against the registry's record, and the
// refusal of a changed key.
package trust

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

// Sources of a pin: the first contact, or the owner with `busctl pin`.
const (
	SourceTOFU   = "tofu"
	SourcePinned = "pinned"
)

// Pin is one line of known_agents. It keeps the public key, not only the fingerprint, so a
// message verifies without the registry.
type Pin struct {
	AgentID     string
	Fingerprint string
	PublicKey   string // base64 of the raw Ed25519 key
	FirstSeen   time.Time
	Source      string
}

// Directory looks up an agent's registry record. Agents cannot read the registry on the broker
// (design.md §4a), so until busd serves it the directory is local: see FileDirectory.
type Directory interface {
	Lookup(ctx context.Context, agentID string) (bus.AgentRecord, error)
}

// ErrUnavailable marks a failure that says nothing about the sender: the registry record could
// not be read. The message is neither accepted nor rejected for it (§6, a failed lookup is never
// an authoritative rejection).
var ErrUnavailable = errors.New("registry record unavailable")

// ChangedKeyError means a known agent signed with a key it has not been pinned with.
type ChangedKeyError struct {
	AgentID, PinnedFingerprint, SeenFingerprint string
}

func (err ChangedKeyError) Error() string {
	return fmt.Sprintf("key of %s changed: pinned %s, message signed with %s; never auto-accepted", err.AgentID, err.PinnedFingerprint, err.SeenFingerprint)
}

// Store is the `known_agents` file of one identity. The adapter is its only writer.
type Store struct {
	path string
	pins map[string][]Pin
}

// Open reads the file; a missing file is an empty store.
func Open(path string) (*Store, error) {
	store := &Store{path: path, pins: map[string][]Pin{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return nil, fmt.Errorf("%s:%d: want 5 fields (agent-id fingerprint public-key first-seen source)", path, lineNumber)
		}
		firstSeen, err := time.Parse(time.RFC3339, fields[3])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		store.remember(Pin{AgentID: fields[0], Fingerprint: fields[1], PublicKey: fields[2], FirstSeen: firstSeen, Source: fields[4]})
	}
	return store, scanner.Err()
}

func (store *Store) remember(pin Pin) {
	store.pins[pin.AgentID] = append(store.pins[pin.AgentID], pin)
}

// Unpinned is a store that holds no pins and must not be added to. Resolving against it trusts the
// registry alone, for a reader that only displays messages, like the monitor (design.md §10a).
func Unpinned() *Store { return &Store{pins: map[string][]Pin{}} }

// Pins returns the keys pinned for the agent.
func (store *Store) Pins(agentID string) []Pin { return store.pins[agentID] }

// Add pins a key and appends it to the file.
func (store *Store) Add(pin Pin) error {
	line := fmt.Sprintf("%s %s %s %s %s\n", pin.AgentID, pin.Fingerprint, pin.PublicKey, pin.FirstSeen.UTC().Format(time.RFC3339), pin.Source)
	file, err := os.OpenFile(store.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, files.SecretMode)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	store.remember(pin)
	return nil
}

// Resolution is the key to verify a message with. NewPin is set for a first contact: the caller
// adds it once the signature has verified, so only a key that proved itself is pinned.
type Resolution struct {
	Key    ed25519.PublicKey
	Source string
	NewPin *Pin
}

// Resolve finds the key an agent's message claims to be signed with (§6 step 3, with
// --auto-accept-new-keys on). A pinned key is used as it is. An unknown agent is looked up in the
// registry, and the key named by keyFingerprint must be in its record and not revoked. A known
// agent whose message names another key is a ChangedKeyError; nothing here follows rotation chains.
func (store *Store) Resolve(ctx context.Context, directory Directory, agentID, keyFingerprint string, now time.Time) (Resolution, error) {
	pins := store.Pins(agentID)
	for _, pin := range pins {
		if pin.Fingerprint == keyFingerprint {
			key, err := decodeKey(pin.PublicKey)
			return Resolution{Key: key, Source: pin.Source}, err
		}
	}
	if len(pins) > 0 {
		return Resolution{}, ChangedKeyError{AgentID: agentID, PinnedFingerprint: pins[len(pins)-1].Fingerprint, SeenFingerprint: keyFingerprint}
	}

	record, err := directory.Lookup(ctx, agentID)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w for %s: %w", ErrUnavailable, agentID, err)
	}
	for _, entry := range keyHistory(record) {
		if entry.Fingerprint != keyFingerprint {
			continue
		}
		if entry.Revoked {
			return Resolution{}, fmt.Errorf("key %s of %s is revoked", keyFingerprint, agentID)
		}
		key, err := decodeKey(entry.PublicKey)
		if err != nil {
			return Resolution{}, err
		}
		if identity.Fingerprint(key) != entry.Fingerprint {
			return Resolution{}, fmt.Errorf("registry record of %s: fingerprint %s does not match its key", agentID, entry.Fingerprint)
		}
		pin := Pin{AgentID: agentID, Fingerprint: entry.Fingerprint, PublicKey: entry.PublicKey, FirstSeen: now, Source: SourceTOFU}
		return Resolution{Key: key, Source: SourceTOFU, NewPin: &pin}, nil
	}
	return Resolution{}, fmt.Errorf("key %s is not in the registry record of %s", keyFingerprint, agentID)
}

// keyHistory reads a record without a keys list as a history of one key (§4).
func keyHistory(record bus.AgentRecord) []bus.KeyEntry {
	if len(record.Keys) > 0 {
		return record.Keys
	}
	return []bus.KeyEntry{{PublicKey: record.PublicKey, Fingerprint: record.Fingerprint}}
}

func decodeKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("malformed public key")
	}
	return raw, nil
}
