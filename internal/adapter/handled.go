// Package adapter is the bus adapter's core (design.md §2): it signs and sends messages, reads
// the agent's channels, verifies what arrives and keeps it until the agent acknowledges it. The
// MCP tools (milestone 4) and the delivery hooks (milestone 5) are thin layers over its API.
package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

// Kinds of handledEntry.
const (
	entryMessage  = "message"  // a verified message that was acked, or that this adapter sent
	entryRejected = "rejected" // a delivery that failed verification, known by its sequence only
)

// handledEntry is one line of the handled file. A rejected delivery has only a destination and a
// sequence: its envelope ID is chosen by whoever sent it, so it must never be remembered (§4).
type handledEntry struct {
	Kind        string    `json:"kind"`
	Destination string    `json:"destination"`
	Sequence    uint64    `json:"sequence"`
	From        string    `json:"from,omitempty"`
	ID          string    `json:"id,omitempty"`
	Digest      string    `json:"digest,omitempty"` // of the signed record, to catch an ID reused with another payload
	Thread      string    `json:"thread,omitempty"`
	Hops        int       `json:"hops,omitempty"`
	At          time.Time `json:"at"`
}

type sequenceKey struct {
	destination string
	sequence    uint64
}

type messageKey struct {
	destination, from, id string
}

// handledSet is the persisted record of what was handled (design.md §4), kept as an append-only
// file of JSON lines that is read into memory at start. An entry is on disk before the caller
// acks the broker, so a crash redelivers mail and never loses it.
type handledSet struct {
	file      *os.File
	sequences map[sequenceKey]bool
	messages  map[messageKey]handledEntry
	byID      map[string]handledEntry // for reply_to lookups, whatever the destination
}

func openHandledSet(path string) (*handledSet, error) {
	set := &handledSet{sequences: map[sequenceKey]bool{}, messages: map[messageKey]handledEntry{}, byID: map[string]handledEntry{}}
	if existing, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(existing)
		for scanner.Scan() {
			var entry handledEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				existing.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			set.remember(entry)
		}
		existing.Close()
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, files.SecretMode)
	if err != nil {
		return nil, err
	}
	set.file = file
	return set, nil
}

func (set *handledSet) remember(entry handledEntry) {
	set.sequences[sequenceKey{entry.Destination, entry.Sequence}] = true
	if entry.Kind != entryMessage {
		return
	}
	key := messageKey{entry.Destination, entry.From, entry.ID}
	if _, known := set.messages[key]; !known {
		set.messages[key] = entry
	}
	if _, known := set.byID[entry.ID]; !known {
		set.byID[entry.ID] = entry
	}
}

// add writes the entry and syncs it to disk before it returns.
func (set *handledSet) add(entry handledEntry) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := set.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := set.file.Sync(); err != nil {
		return err
	}
	set.remember(entry)
	return nil
}

func (set *handledSet) sequenceHandled(destination string, sequence uint64) bool {
	return set.sequences[sequenceKey{destination, sequence}]
}

func (set *handledSet) message(destination, from, id string) (handledEntry, bool) {
	entry, ok := set.messages[messageKey{destination, from, id}]
	return entry, ok
}

func (set *handledSet) messageByID(id string) (handledEntry, bool) {
	entry, ok := set.byID[id]
	return entry, ok
}

func (set *handledSet) close() error { return set.file.Close() }
