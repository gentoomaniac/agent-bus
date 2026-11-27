package monitor

import (
	"context"
	"errors"
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
