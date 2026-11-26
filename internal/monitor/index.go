package monitor

import (
	"cmp"
	"slices"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// The index holds what the monitor needs to know about every message without its body
// (docs/design/lazy-monitor.md): where it is, who sent it, to whom, when and with what
// verification. Bodies are read from the stream when a client asks for them.

// row is one message of the index, about 90 bytes. Strings are interned: the ID of one is a
// number into the index's name table.
type row struct {
	sequence  uint64
	time      int64    // Unix nanoseconds; zero for a record that is no envelope
	id        [16]byte // the ULID's bits; zero for a record that is no envelope
	thread    uint32
	from      uint32
	to        uint32
	kind      uint32 // the message type
	targetsAt uint32 // where the targets start in the name table's pool
	targetsN  uint16
	status    uint8 // an index into verifications
}

// verifications are the values of row.status.
var verifications = []Verification{Rejected, Verified, UnknownSender}

func statusCode(status Verification) uint8 {
	return uint8(slices.Index(verifications, status))
}

// index holds the rows of every channel and finds a message by its ID. The Monitor's mutex
// guards it.
type index struct {
	names    names
	channels map[string][]row // by channel ID, ordered by sequence
	byID     map[[16]byte]position
	reasons  map[uint64]string // why a message is not verified, by sequence; few messages have one
}

// position finds a row by its channel and sequence, which do not change when later messages are
// inserted before it.
type position struct {
	channel  string
	sequence uint64
}

func newIndex() *index {
	return &index{names: newNames(), channels: map[string][]row{}, byID: map[[16]byte]position{}, reasons: map[uint64]string{}}
}

// add inserts the message in its channel's order. It says false for one that is there already.
func (index *index) add(message Message) bool {
	rows := index.channels[message.Channel]
	at, found := slices.BinarySearchFunc(rows, message.Sequence, compareRowSequence)
	if found {
		return false
	}
	entry := row{
		sequence: message.Sequence,
		thread:   index.names.id(message.Thread),
		from:     index.names.id(message.From),
		to:       index.names.id(message.To),
		kind:     index.names.id(message.Type),
		status:   statusCode(message.Status),
	}
	entry.targetsAt, entry.targetsN = index.names.addList(message.Targets)
	if !message.Time.IsZero() {
		entry.time = message.Time.UnixNano()
	}
	if id, ok := envelope.ParseID(message.ID); ok {
		entry.id = id
		index.byID[id] = position{message.Channel, message.Sequence}
	}
	if message.Reason != "" {
		index.reasons[message.Sequence] = message.Reason
	}
	index.channels[message.Channel] = slices.Insert(rows, at, entry)
	return true
}

func compareRowSequence(entry row, sequence uint64) int {
	return cmp.Compare(entry.sequence, sequence)
}

// remove drops the row of the message at the sequence in the channel, and the channel with its
// last row.
func (index *index) remove(channel string, sequence uint64) {
	rows := index.channels[channel]
	at, found := slices.BinarySearchFunc(rows, sequence, compareRowSequence)
	if !found {
		return
	}
	index.forget(rows[at])
	index.channels[channel] = slices.Delete(rows, at, at+1)
	if len(index.channels[channel]) == 0 {
		delete(index.channels, channel)
	}
}

// dropBelow drops every row with a sequence below the first one, which is how retention deletes:
// from the front of the log. It says how many it dropped.
func (index *index) dropBelow(first uint64) (dropped int) {
	for channel, rows := range index.channels {
		cut, _ := slices.BinarySearchFunc(rows, first, compareRowSequence)
		for _, entry := range rows[:cut] {
			index.forget(entry)
		}
		dropped += cut
		if cut == len(rows) {
			delete(index.channels, channel)
		} else if cut > 0 {
			index.channels[channel] = slices.Delete(rows, 0, cut)
		}
	}
	return dropped
}

// forget removes what a row has outside its channel's list: its ID and its reason.
func (index *index) forget(entry row) {
	if entry.id != [16]byte{} {
		delete(index.byID, entry.id)
	}
	delete(index.reasons, entry.sequence)
}

// countThrough says how many rows have a sequence of at most last.
func (index *index) countThrough(last uint64) (count int) {
	for _, rows := range index.channels {
		end, found := slices.BinarySearchFunc(rows, last, compareRowSequence)
		if found {
			end++
		}
		count += end
	}
	return count
}

// find returns the row of the message at the sequence in the channel.
func (index *index) find(channel string, sequence uint64) (row, bool) {
	rows := index.channels[channel]
	at, found := slices.BinarySearchFunc(rows, sequence, compareRowSequence)
	if !found {
		return row{}, false
	}
	return rows[at], true
}

// lookup finds the message with the envelope ID.
func (index *index) lookup(id string) (position, bool) {
	raw, ok := envelope.ParseID(id)
	if !ok {
		return position{}, false
	}
	at, known := index.byID[raw]
	return at, known
}

// describeAll returns the messages as far as the index knows them: everything but the body and
// the ID each replies to.
func (index *index) describeAll(channel string, rows []row) []Message {
	messages := make([]Message, len(rows))
	for i, entry := range rows {
		messages[i] = index.describe(channel, entry)
	}
	return messages
}

func (index *index) describe(channel string, entry row) Message {
	message := Message{
		Sequence: entry.sequence, Channel: channel, Status: verifications[entry.status], Reason: index.reasons[entry.sequence],
		From: index.names.str(entry.from), To: index.names.str(entry.to), Type: index.names.str(entry.kind),
		Thread: index.names.str(entry.thread), Targets: index.names.list(entry.targetsAt, entry.targetsN),
	}
	if entry.time != 0 {
		message.Time = time.Unix(0, entry.time).UTC()
	}
	if entry.id != [16]byte{} {
		message.ID = envelope.FormatID(entry.id)
	}
	return message
}

// names interns strings: each distinct one is kept once and known by a number. Number 0 is the
// empty string.
type names struct {
	ids  map[string]uint32
	text []string
	pool []uint32 // the target lists of all rows, one after the other
}

func newNames() names {
	return names{ids: map[string]uint32{"": 0}, text: []string{""}}
}

func (names *names) id(text string) uint32 {
	if id, known := names.ids[text]; known {
		return id
	}
	id := uint32(len(names.text))
	names.ids[text] = id
	names.text = append(names.text, text)
	return id
}

func (names *names) str(id uint32) string { return names.text[id] }

// addList stores a list of strings in the pool and says where.
func (names *names) addList(list []string) (at uint32, n uint16) {
	at = uint32(len(names.pool))
	for _, text := range list {
		names.pool = append(names.pool, names.id(text))
	}
	return at, uint16(len(list))
}

// list returns the strings addList stored; nil for an empty list, as a Message holds it.
func (names *names) list(at uint32, n uint16) []string {
	if n == 0 {
		return nil
	}
	list := make([]string, n)
	for i, id := range names.pool[at : at+uint32(n)] {
		list[i] = names.text[id]
	}
	return list
}
