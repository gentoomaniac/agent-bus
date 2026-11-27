package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go, so the binary stays CGO_ENABLED=0

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// The store is the monitor's state file (docs/design/lazy-monitor.md): one SQLite file that keeps
// the index rows, so a restart loads them and reads only the stream's tail. It is a cache. The
// stream is the source of truth, so a file that is missing, unreadable or of another schema is
// deleted and rebuilt by one full read of the stream, and a file that cannot be written only costs
// the next start that read.

const (
	// storeSchema is bumped by a release that changes the tables below; the file is then rebuilt,
	// not migrated.
	storeSchema = "2"
	// storeBatchRows and storeBatchInterval bound how long a message waits in memory before it is
	// written: a batch is one transaction.
	storeBatchRows     = 1000
	storeBatchInterval = 500 * time.Millisecond
	// storeDeleteRows is how many rows one transaction of retention deletes, so the writer is
	// never held for long.
	storeDeleteRows = 5000
)

// message_fts has the words of the messages, rowid = message.seq. It keeps no text: a hit is a
// sequence, and the body comes from the cache or the stream. The refs are not in it; a search finds
// words of the title and the text.
//
// The cursor table holds, per source (channel subject), the last sequence read from it. A message
// row cannot tell it: a DM channel merges two sources, and one source may be behind another when
// the monitor stops, so a single "last sequence" would skip the records of the slower one.
const storeTables = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE message (
  seq INTEGER PRIMARY KEY, channel TEXT NOT NULL, id BLOB, thread TEXT NOT NULL, from_agent TEXT NOT NULL,
  to_name TEXT NOT NULL, targets TEXT NOT NULL, ts INTEGER NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL,
  reason TEXT NOT NULL
) WITHOUT ROWID;
CREATE VIRTUAL TABLE message_fts USING fts5(
  title, text, content = '', contentless_delete = 1, tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TABLE cursor (source TEXT PRIMARY KEY, seq INTEGER NOT NULL) WITHOUT ROWID;
`

type store struct {
	write, read *sql.DB // the same database in memory, where a second connection would see another one
	report      func(error)

	// writeMu lets one writer at a time through: a flush, a delete, a wipe. Readers need no lock.
	writeMu sync.Mutex

	mu       sync.Mutex
	pending  []Message         // queued, not written yet
	inflight []Message         // in the transaction being written
	cursors  map[string]uint64 // by source: the last sequence read, written or not
	dirty    map[string]uint64 // the cursors the next flush writes
	failed   error             // the write that failed: nothing more is written until the next start
	fresh    bool              // the file held nothing when it was opened
}

// openStore opens the file at path, which may not exist yet, and loads its rows into a new index.
// A file it cannot use is deleted and made again, and the error reported. Without a path, or when
// not even a new file works, the store lives in memory: the monitor then reads the whole stream at
// every start, as it did before the file.
func openStore(path string, report func(error)) (*store, *index) {
	if path != "" {
		opened, loaded, err := openStoreFile(path, report)
		if err != nil {
			report(fmt.Errorf("the state file %s is unusable, rebuilding it from the stream: %w", path, err))
			for _, name := range []string{path, path + "-wal", path + "-shm"} {
				os.Remove(name)
			}
			opened, loaded, err = openStoreFile(path, report)
		}
		if err == nil {
			return opened, loaded
		}
		report(fmt.Errorf("the state file %s cannot be created, keeping the index in memory: %w", path, err))
	}
	opened, loaded, err := newStore(":memory:", ":memory:", report)
	if err != nil {
		panic(err) // an empty in-memory database does not fail
	}
	return opened, loaded
}

func openStoreFile(path string, report func(error)) (*store, *index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	file := url.URL{Scheme: "file", Path: path}
	pragmas := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	return newStore(file.String()+"?"+pragmas, file.String()+"?"+pragmas+"&_pragma=query_only(1)", report)
}

// newStore connects, checks or creates the schema and loads the rows. A readDSN equal to the
// writeDSN means one shared connection.
func newStore(writeDSN, readDSN string, report func(error)) (*store, *index, error) {
	s := &store{report: report, cursors: map[string]uint64{}, dirty: map[string]uint64{}}
	var err error
	if s.write, err = sql.Open("sqlite", writeDSN); err != nil {
		return nil, nil, err
	}
	s.write.SetMaxOpenConns(1)
	s.read = s.write
	if readDSN != writeDSN {
		if s.read, err = sql.Open("sqlite", readDSN); err != nil {
			s.write.Close()
			return nil, nil, err
		}
	}
	loaded, err := s.prepare()
	if err != nil {
		s.closeDatabases()
		return nil, nil, err
	}
	s.fresh = len(s.cursors) == 0
	return s, loaded, nil
}

// prepare creates the tables of a new file, or checks an old one and loads it.
func (s *store) prepare() (*index, error) {
	var tables int
	if err := s.write.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'meta'`).Scan(&tables); err != nil {
		return nil, err
	}
	if tables == 0 {
		if _, err := s.write.Exec(storeTables); err != nil {
			return nil, err
		}
		_, err := s.write.Exec(`INSERT INTO meta VALUES ('schema', ?)`, storeSchema)
		return newIndex(), err
	}
	var schema string
	if err := s.write.QueryRow(`SELECT value FROM meta WHERE key = 'schema'`).Scan(&schema); err != nil {
		return nil, err
	}
	if schema != storeSchema {
		return nil, fmt.Errorf("schema %s, this version writes %s", schema, storeSchema)
	}
	// Only the message table: checking the FTS index as well takes a second per 100 000 messages.
	// A damaged index shows as a failed search, and deleting the file repairs it.
	var verdict string
	if err := s.write.QueryRow(`PRAGMA quick_check(message)`).Scan(&verdict); err != nil || verdict != "ok" {
		return nil, fmt.Errorf("integrity check says %q: %v", verdict, err)
	}
	return s.load()
}

// load builds the index from the stored rows and reads the cursors.
func (s *store) load() (*index, error) {
	loaded := newIndex()
	rows, err := s.read.Query(`SELECT seq, channel, id, thread, from_agent, to_name, targets, ts, type, status FROM message ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			message         Message
			id              []byte
			targets, status string
			nanos           int64
		)
		err := rows.Scan(&message.Sequence, &message.Channel, &id, &message.Thread, &message.From, &message.To, &targets, &nanos, &message.Type, &status)
		if err != nil {
			return nil, err
		}
		message.Status = Verification(status)
		if !slices.Contains(verifications, message.Status) {
			return nil, fmt.Errorf("message %d: unknown status %q", message.Sequence, status)
		}
		if len(id) == 16 {
			message.ID = envelope.FormatID([16]byte(id))
		}
		if targets != "" {
			if err := json.Unmarshal([]byte(targets), &message.Targets); err != nil {
				return nil, fmt.Errorf("message %d: %w", message.Sequence, err)
			}
		}
		if nanos != 0 {
			message.Time = time.Unix(0, nanos).UTC()
		}
		loaded.add(message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cursors, err := s.read.Query(`SELECT source, seq FROM cursor`)
	if err != nil {
		return nil, err
	}
	defer cursors.Close()
	for cursors.Next() {
		var source string
		var sequence uint64
		if err := cursors.Scan(&source, &sequence); err != nil {
			return nil, err
		}
		s.cursors[source] = sequence
	}
	return loaded, cursors.Err()
}

// cursor is the last sequence read from the source before, or 0.
func (s *store) cursor(source string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[source]
}

// advance records that the source was read through the sequence. The follower has queued the
// messages up to it already, and a flush takes the cursor with the rows queued before it, in one
// transaction, so a crash leaves rows and cursors in step.
func (s *store) advance(source string, sequence uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[source], s.dirty[source] = sequence, sequence
}

// enqueue schedules a message for the next write. When the queue is full it writes the batch
// itself, which holds the caller back: the in-memory index is never behind, the file may be.
func (s *store) enqueue(message Message) {
	s.mu.Lock()
	if s.failed != nil {
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, message)
	full := len(s.pending) >= storeBatchRows
	s.mu.Unlock()
	if full {
		s.flush()
	}
}

// run writes the queue every storeBatchInterval until ctx ends, and once more then.
func (s *store) run(ctx context.Context) {
	ticker := time.NewTicker(storeBatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flush()
			return
		case <-ticker.C:
			s.flush()
		}
	}
}

// flush writes the queued messages and cursors in one transaction. A failed write stops all writing
// until the next start: the file then ends at the last transaction that worked, with its cursors,
// and the start reads the stream from there, whereas going on would leave a hole behind the cursors.
func (s *store) flush() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	s.inflight, s.pending = s.pending, nil
	messages := s.inflight
	cursors := s.dirty
	s.dirty = map[string]uint64{}
	s.mu.Unlock()
	if len(messages) == 0 && len(cursors) == 0 {
		return
	}

	err := s.writeBatch(messages, cursors)
	s.mu.Lock()
	s.inflight = nil
	if err != nil {
		s.failed, s.pending = err, nil
	}
	s.mu.Unlock()
	if err != nil {
		s.report(fmt.Errorf("writing the state file failed, the next start reads the stream again from the last write: %w", err))
	}
}

func (s *store) writeBatch(messages []Message, cursors map[string]uint64) error {
	tx, err := s.write.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert, err := tx.Prepare(`INSERT OR REPLACE INTO message VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	insertWords, err := tx.Prepare(`INSERT INTO message_fts (rowid, title, text) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insertWords.Close()
	for _, message := range messages {
		var id []byte
		if raw, ok := envelope.ParseID(message.ID); ok {
			id = raw[:]
		}
		var targets string
		if len(message.Targets) > 0 {
			encoded, _ := json.Marshal(message.Targets)
			targets = string(encoded)
		}
		var nanos int64
		if !message.Time.IsZero() {
			nanos = message.Time.UnixNano()
		}
		_, err := insert.Exec(message.Sequence, message.Channel, id, message.Thread, message.From, message.To, targets, nanos,
			message.Type, string(message.Status), message.Reason)
		if err != nil {
			return err
		}
		if message.Title == "" && message.Text == "" {
			continue
		}
		if _, err := insertWords.Exec(message.Sequence, message.Title, message.Text); err != nil {
			return err
		}
	}
	for source, sequence := range cursors {
		if _, err := tx.Exec(`INSERT INTO cursor VALUES (?, ?) ON CONFLICT (source) DO UPDATE SET seq = excluded.seq`, source, sequence); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// failure is the error of the write that stopped all writing, or nil: the file, and so the word
// search, is behind the monitor from then on.
func (s *store) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// reason is why the message at the sequence is not verified; the index does not keep it. A message
// still queued is answered from the queue.
func (s *store) reason(sequence uint64) string {
	s.mu.Lock()
	for _, queue := range [][]Message{s.pending, s.inflight} {
		for _, message := range queue {
			if message.Sequence == sequence {
				s.mu.Unlock()
				return message.Reason
			}
		}
	}
	s.mu.Unlock()
	var reason string
	if err := s.read.QueryRow(`SELECT reason FROM message WHERE seq = ?`, sequence).Scan(&reason); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.report(err)
	}
	return reason
}

// eachMatch calls visit with the channel and sequence of the messages that match the FTS
// expression, newest first, below the sequence, until it says false. It writes what is queued
// first, so a message that has just arrived is found.
func (s *store) eachMatch(ctx context.Context, expression string, below uint64, visit func(channel string, sequence uint64) bool) error {
	s.flush()
	rows, err := s.read.QueryContext(ctx, `SELECT message.seq, message.channel FROM message_fts JOIN message ON message.seq = message_fts.rowid
		WHERE message_fts MATCH ? AND message_fts.rowid < ? ORDER BY message_fts.rowid DESC`, expression, below)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var channel string
		var sequence uint64
		if err := rows.Scan(&sequence, &channel); err != nil {
			return err
		}
		if !visit(channel, sequence) {
			break
		}
	}
	return rows.Err()
}

// remove deletes the messages with the sequences, which the stream no longer has.
func (s *store) remove(sequences []uint64) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.dropQueued(func(sequence uint64) bool { return slices.Contains(sequences, sequence) })
	for _, sequence := range sequences {
		for _, table := range []string{"message_fts WHERE rowid", "message WHERE seq"} {
			if _, err := s.write.Exec(`DELETE FROM `+table+` = ?`, sequence); err != nil {
				s.report(err)
			}
		}
	}
}

// removeBelow deletes the messages with a sequence below the first one, in batches so the writer
// is free in between.
func (s *store) removeBelow(first uint64) {
	s.writeMu.Lock()
	s.dropQueued(func(sequence uint64) bool { return sequence < first })
	s.writeMu.Unlock()
	for more := true; more; {
		s.writeMu.Lock()
		deleted, err := s.removeBatchBelow(first)
		s.writeMu.Unlock()
		if err != nil {
			s.report(err)
			return
		}
		more = deleted == storeDeleteRows
	}
}

// removeBatchBelow deletes the oldest storeDeleteRows messages below the first sequence, or the
// rest if there are fewer, and says how many.
func (s *store) removeBatchBelow(first uint64) (deleted int64, err error) {
	tx, err := s.write.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const oldest = `(SELECT seq FROM message WHERE seq < ? ORDER BY seq LIMIT ?)`
	if _, err := tx.Exec(`DELETE FROM message_fts WHERE rowid IN `+oldest, first, storeDeleteRows); err != nil {
		return 0, err
	}
	result, err := tx.Exec(`DELETE FROM message WHERE seq IN `+oldest, first, storeDeleteRows)
	if err != nil {
		return 0, err
	}
	if deleted, err = result.RowsAffected(); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

// dropQueued forgets the queued messages that isGone names. The caller holds writeMu, so none is
// in a transaction.
func (s *store) dropQueued(isGone func(sequence uint64) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(message Message) bool { return isGone(message.Sequence) })
}

// wipe forgets everything: the stream has started over and the file describes another one.
func (s *store) wipe() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	s.pending, s.cursors, s.dirty, s.failed = nil, map[string]uint64{}, map[string]uint64{}, nil
	s.mu.Unlock()
	for _, table := range []string{"message_fts", "message", "cursor"} {
		if _, err := s.write.Exec(`DELETE FROM ` + table); err != nil {
			s.report(err)
		}
	}
}

// close writes what is queued and closes the file.
func (s *store) close() {
	s.flush()
	s.closeDatabases()
}

func (s *store) closeDatabases() {
	s.read.Close()
	if s.write != s.read {
		s.write.Close()
	}
}
