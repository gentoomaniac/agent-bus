package monitor

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func isIndexing(monitor *Monitor) bool {
	query, _ := ParseQuery("cake")
	_, err := monitor.Search(context.Background(), query, 10, "")
	return errors.As(err, new(ErrIndexing))
}

// flakyRegistry fails the registry read while fail is set.
type flakyRegistry struct {
	*fakeBus
	fail atomic.Bool
}

func (registry *flakyRegistry) Agents(ctx context.Context) ([]bus.AgentRecord, error) {
	if registry.fail.Load() {
		return nil, errors.New("registry down")
	}
	return registry.fakeBus.Agents(ctx)
}

func TestSearchWaitsForADiscoveryThatSawEveryAgent(t *testing.T) {
	alice, bob := newTestAgent("api-client@alice"), newTestAgent("lib-x@bob")
	fake := newFakeBus(t, alice, bob)
	fake.store(bus.GeneralChannel(), 10, alice.sign(t, bus.GeneralChannel(), "cake in general"))
	fake.store(bus.DMChannel(alice.id), 20, bob.sign(t, bus.DMChannel(alice.id), "cake in a dm"))
	registry := &flakyRegistry{fakeBus: fake}
	registry.fail.Store(true)

	monitor := New(Config{Admin: registry, History: fake, Report: func(error) {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitForMessages(t, monitor, "general", 1)
	time.Sleep(3 * emptyReadPause) // general has been read to its end by now
	if !isIndexing(monitor) {
		t.Fatal("search answers although the agents' DM channels are not known yet")
	}

	registry.fail.Store(false)
	monitor.headSeen <- struct{}{}
	waitUntil(t, "the end of indexing", func() bool { return !isIndexing(monitor) })
	if got := sequencesFound(t, monitor, "cake"); !slices.Equal(got, []uint64{20, 10}) {
		t.Errorf("found %v, want both messages, the DM too", got)
	}
}

// gatedHistory holds back the history of one channel until the gate is closed.
type gatedHistory struct {
	*fakeBus
	gated bus.Channel
	gate  chan struct{}
}

func (history *gatedHistory) History(ctx context.Context, from bus.Channel, after uint64, limit int) ([]bus.Record, error) {
	if from.String() == history.gated.String() {
		select {
		case <-history.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return history.fakeBus.History(ctx, from, after, limit)
}

func TestASourceTheFileKnowsNothingOfIsFilledBeforeSearchAnswers(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	fake.store(bus.GeneralChannel(), 10, alice.sign(t, bus.GeneralChannel(), "cake in general"))
	path := filepath.Join(t.TempDir(), "index.sqlite")
	first, _, stop := storedMonitor(t, fake, path, failOnError(t))
	waitForMessages(t, first, "general", 1)
	stop()

	// A group channel appears while the monitor is down, with a message from before its restart.
	group := bus.GroupChannel("ops")
	fake.groups = append(fake.groups, bus.TopicRecord{Name: "ops"})
	fake.store(group, 20, alice.sign(t, group, "cake in a group"))
	history := &gatedHistory{fakeBus: fake, gated: group, gate: make(chan struct{})}
	monitor := New(Config{Admin: fake, History: history, IndexPath: path, Report: failOnError(t)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitUntil(t, "search to wait for the new group", func() bool { return isIndexing(monitor) })
	close(history.gate)
	waitUntil(t, "the end of indexing", func() bool { return !isIndexing(monitor) })
	if got := sequencesFound(t, monitor, "cake"); !slices.Equal(got, []uint64{20, 10}) {
		t.Errorf("found %v, want the group's message too", got)
	}
}

func TestSearchSaysSoWhenTheStateFileCannotBeWritten(t *testing.T) {
	var reported atomic.Int32
	monitor := New(Config{History: newFakeBus(t), Report: func(error) { reported.Add(1) }})
	server := serve(monitor)
	defer server.Close()
	monitor.add(Message{Sequence: 1, Channel: "general", Status: Verified, From: "alice@home", Text: "cake"})
	if got := sequencesFound(t, monitor, "cake"); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("setup: found %v", got)
	}

	monitor.store.write.Close() // the disk goes away
	monitor.add(Message{Sequence: 2, Channel: "general", Status: Verified, From: "alice@home", Text: "cake again"})
	monitor.store.flush()
	if reported.Load() == 0 {
		t.Error("the failed write was not reported")
	}

	query, _ := ParseQuery("cake")
	if _, err := monitor.Search(context.Background(), query, 10, ""); !errors.Is(err, ErrIndexFailed) {
		t.Errorf("a search for words: %v, want ErrIndexFailed", err)
	}
	response, body := call(t, server, "GET", "/api/v1/search?q=cake", "", nil)
	if response.StatusCode != http.StatusServiceUnavailable || errorCode(body) != "index_failed" {
		t.Errorf("answer %d %s, want 503 index_failed", response.StatusCode, body)
	}
	if got := sequencesFound(t, monitor, "from:alice"); !slices.Equal(got, []uint64{2, 1}) {
		t.Errorf("a search by filters found %v, want both messages: it does not need the file", got)
	}
	if status := monitor.Status(); status.Search != "index_failed" {
		t.Errorf("status.search = %q, want index_failed", status.Search)
	}
}
