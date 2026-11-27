package monitor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// historySpy records from where each source is read.
type historySpy struct {
	*fakeBus
	mu     sync.Mutex
	starts map[string][]uint64 // the "after" of every History call, by channel
}

func (spy *historySpy) History(ctx context.Context, from bus.Channel, after uint64, limit int) ([]bus.Record, error) {
	spy.mu.Lock()
	spy.starts[from.String()] = append(spy.starts[from.String()], after)
	spy.mu.Unlock()
	return spy.fakeBus.History(ctx, from, after, limit)
}

func (spy *historySpy) firstStart(channel bus.Channel) uint64 {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	starts := spy.starts[channel.String()]
	if len(starts) == 0 {
		return 0
	}
	return starts[0]
}

// storedMonitor follows the fake bus with a state file at path until stop is called, which also
// waits for the monitor to write the file and close it.
func storedMonitor(t *testing.T, fake *fakeBus, path string, report func(error)) (monitor *Monitor, spy *historySpy, stop func()) {
	t.Helper()
	spy = &historySpy{fakeBus: fake, starts: map[string][]uint64{}}
	monitor = New(Config{Admin: fake, History: spy, IndexPath: path, Report: report})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return monitor, spy, stop
}

func failOnError(t *testing.T) func(error) {
	return func(err error) { t.Error(err) }
}

func (s *store) countRows(t *testing.T) (count int) {
	t.Helper()
	if err := s.read.QueryRow(`SELECT count(*) FROM message`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestARestartLoadsTheFileAndReadsOnlyTheTail(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	general, group := bus.GeneralChannel(), bus.GroupChannel("ops")
	fake.groups = append(fake.groups, bus.TopicRecord{Name: "ops"})
	fake.store(general, 10, alice.sign(t, general, "one"))
	fake.store(group, 20, alice.sign(t, group, "two"))
	fake.store(general, 30, alice.sign(t, general, "three"))
	path := filepath.Join(t.TempDir(), "monitor-web", "index.sqlite")

	first, _, stop := storedMonitor(t, fake, path, failOnError(t))
	waitForMessages(t, first, "general", 2)
	waitForMessages(t, first, "group:ops", 1)
	stop()

	fake.store(group, 40, alice.sign(t, group, "four"))
	loaded := New(Config{Admin: fake, History: fake, IndexPath: path, Report: failOnError(t)})
	if channels := loaded.Channels(); channels[0].Messages != 2 || channels[0].LastSequence != 30 {
		t.Fatalf("before reading anything the monitor holds %+v, want the two messages of the file", channels[0])
	}
	loaded.store.close()

	second, spy, _ := storedMonitor(t, fake, path, failOnError(t))
	if got := waitForMessages(t, second, "group:ops", 2); got[1].Text != "four" || got[0].Text != "two" {
		t.Errorf("group:ops = %+v, want the stored message and the new one", got)
	}
	if got := messagesOf(t, second, "general", 0, 10); len(got) != 2 || got[0].Text != "one" || got[0].From != "api-client@alice" || got[0].Status != Verified || len(got[0].Targets) != 1 {
		t.Errorf("general = %+v, want the stored messages, their bodies read from the stream", got)
	}
	if spy.firstStart(general) != 30 || spy.firstStart(group) != 20 {
		t.Errorf("history read from %d (general) and %d (group:ops), want the sources' last sequences 30 and 20", spy.firstStart(general), spy.firstStart(group))
	}
}

func TestASourceBehindAnotherIsReadFromItsOwnCursor(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	general, group := bus.GeneralChannel(), bus.GroupChannel("ops")
	fake.groups = append(fake.groups, bus.TopicRecord{Name: "ops"})
	fake.store(group, 10, alice.sign(t, group, "slow"))
	fake.store(general, 20, alice.sign(t, general, "fast"))
	path := filepath.Join(t.TempDir(), "index.sqlite")

	// The monitor stopped after reading general up to 20 but before it read the group at all.
	store, _ := openStore(path, failOnError(t))
	store.advance(general.String(), 20)
	store.close()

	monitor, _, _ := storedMonitor(t, fake, path, failOnError(t))
	if got := waitForMessages(t, monitor, "group:ops", 1); got[0].Text != "slow" {
		t.Errorf("group:ops = %+v", got)
	}
}

func TestAnUnusableFileIsRebuiltFromTheStream(t *testing.T) {
	garbage := func(t *testing.T, path string) {
		if err := os.WriteFile(path, []byte(strings.Repeat("this is not a database ", 500)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	otherSchema := func(t *testing.T, path string) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`UPDATE meta SET value = '0' WHERE key = 'schema'`); err != nil {
			t.Fatal(err)
		}
	}
	for name, damage := range map[string]func(*testing.T, string){"garbage": garbage, "another schema": otherSchema} {
		t.Run(name, func(t *testing.T) {
			alice := newTestAgent("api-client@alice")
			fake := newFakeBus(t, alice)
			fake.store(bus.GeneralChannel(), 10, alice.sign(t, bus.GeneralChannel(), "one"))
			path := filepath.Join(t.TempDir(), "index.sqlite")
			_, _, stop := storedMonitor(t, fake, path, failOnError(t))
			stop()
			damage(t, path)

			var reported []error
			var mu sync.Mutex
			monitor, _, _ := storedMonitor(t, fake, path, func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
			if got := waitForMessages(t, monitor, "general", 1); got[0].Text != "one" {
				t.Errorf("general = %+v, want the message read again from the stream", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(reported) == 0 || !strings.Contains(reported[0].Error(), "rebuilding it from the stream") {
				t.Errorf("reported %v, want a note that the file is rebuilt", reported)
			}
		})
	}
}

func TestAFileThatCannotBeCreatedFallsBackToMemory(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var reported []error
	monitor := New(Config{IndexPath: filepath.Join(blocked, "index.sqlite"), Report: func(err error) { reported = append(reported, err) }})
	monitor.add(Message{Sequence: 1, Channel: "general", Status: Verified, From: "a@b"})
	if len(reported) == 0 || monitor.Channels()[0].Messages != 1 {
		t.Errorf("reported %v, channels %+v; want a report and a working monitor", reported, monitor.Channels())
	}
}

func TestMessagesAreWrittenInBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	store, _ := openStore(path, failOnError(t))
	for sequence := uint64(1); sequence <= storeBatchRows+1; sequence++ {
		store.enqueue(Message{Sequence: sequence, Channel: "general", Status: Verified})
	}
	if written := store.countRows(t); written != storeBatchRows {
		t.Errorf("%d rows written, want a full batch with one still waiting", written)
	}
	store.close()
	loaded, index := openStore(path, failOnError(t))
	defer loaded.close()
	if count := index.countThrough(1 << 40); count != storeBatchRows+1 {
		t.Errorf("%d rows after close, want all of them", count)
	}
}

func TestRetentionDropsRowsFromTheFile(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	for i := 1; i <= 6; i++ {
		fake.store(bus.GeneralChannel(), uint64(i*10), alice.sign(t, bus.GeneralChannel(), "m"+strconv.Itoa(i)))
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	monitor, _, stop := storedMonitor(t, fake, path, failOnError(t))
	waitForMessages(t, monitor, "general", 6)

	fake.remove(10, 20) // from the front: retention
	fake.remove(40)     // from the middle: found gone by a read
	if err := monitor.followLog(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop()

	reopened, index := openStore(path, failOnError(t))
	defer reopened.close()
	if got := index.countThrough(1 << 40); got != 3 || reopened.countRows(t) != 3 {
		t.Errorf("the file holds %d rows (%d in the index), want 30, 50 and 60", reopened.countRows(t), got)
	}
}

func TestRetentionDeletesInBatches(t *testing.T) {
	store, _ := openStore("", failOnError(t))
	defer store.close()
	const count = 2*storeDeleteRows + 100
	for sequence := uint64(1); sequence <= count; sequence++ {
		store.enqueue(Message{Sequence: sequence, Channel: "general", Status: Verified})
	}
	store.flush()
	store.removeBelow(count - 9)
	if got := store.countRows(t); got != 10 {
		t.Errorf("%d rows left, want the last 10", got)
	}
}

func TestAMessageRemovedBeforeItIsWrittenIsNotWritten(t *testing.T) {
	store, _ := openStore("", failOnError(t))
	defer store.close()
	store.enqueue(Message{Sequence: 1, Channel: "general", Status: Verified})
	store.enqueue(Message{Sequence: 2, Channel: "general", Status: Verified})
	store.remove([]uint64{1})
	store.flush()
	if got := store.countRows(t); got != 1 {
		t.Errorf("%d rows, want only message 2", got)
	}
}

func TestTheReasonOfAMessageOutlivesTheCacheAndTheRestart(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 10, []byte("not json"))
	for i := 2; i <= 6; i++ {
		fake.store(bus.GeneralChannel(), uint64(i*10), alice.sign(t, bus.GeneralChannel(), "m"+strconv.Itoa(i)))
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	// The cache holds a few messages, so the rejected one is read from the stream again.
	small := Config{Admin: fake, History: fake, IndexPath: path, Report: failOnError(t), CacheBytes: 2 * messageCost(Message{Text: "m2"})}

	for start := 1; start <= 2; start++ {
		monitor := New(small)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { monitor.Run(ctx); close(done) }()
		waitForMessages(t, monitor, "general", 6)
		first := messagesOf(t, monitor, "general", 0, 1)[0]
		if first.Status != Rejected || first.Reason == "" {
			t.Errorf("start %d: the first message is %q with reason %q, want a rejected one with its reason", start, first.Status, first.Reason)
		}
		if raw, _, err := monitor.RawMessage(context.Background(), "general", 10); err != nil || raw.Reason == "" {
			t.Errorf("start %d: raw message %+v, %v, want its reason", start, raw, err)
		}
		cancel()
		<-done
	}
}

func TestALogBehindTheIndexStartsItOver(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	for i := 1; i <= 3; i++ {
		fake.store(bus.GeneralChannel(), uint64(i*100), alice.sign(t, bus.GeneralChannel(), "old"+strconv.Itoa(i)))
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	monitor, _, stop := storedMonitor(t, fake, path, failOnError(t))
	waitForMessages(t, monitor, "general", 3)

	// The stream is recreated and holds fewer, lower-numbered records.
	fake.remove(100, 200, 300)
	fake.store(bus.GeneralChannel(), 1, alice.sign(t, bus.GeneralChannel(), "new"))
	if err := monitor.followLog(context.Background()); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got := messagesOf(t, monitor, "general", 0, 10)
		if len(got) == 1 && got[0].Text == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("general = %+v, want only the message of the new stream", got)
		}
	}
	stop()
	reopened, _ := openStore(path, failOnError(t))
	defer reopened.close()
	if reopened.countRows(t) != 1 {
		t.Errorf("the file holds %d rows, want 1", reopened.countRows(t))
	}
}
