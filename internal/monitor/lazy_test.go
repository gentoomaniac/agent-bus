package monitor

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// streamReads is a transport whose stream reads are counted and can be made to fail.
type streamReads struct {
	*fakeBus
	reads atomic.Int64
	fail  atomic.Pointer[error]
}

func (stream *streamReads) Message(ctx context.Context, sequence uint64) (bus.Record, error) {
	stream.reads.Add(1)
	if failure := stream.fail.Load(); failure != nil {
		return bus.Record{}, *failure
	}
	return stream.fakeBus.Message(ctx, sequence)
}

// lazyMonitor follows a general channel of count messages "m1".."mN" with sequences 10, 20, ...
// through a cache that holds only a few of them, and waits until all are indexed.
func lazyMonitor(t *testing.T, count int) (*Monitor, *streamReads) {
	t.Helper()
	alice := newTestAgent("api-client@alice")
	fake := newFakeBus(t, alice)
	for i := 1; i <= count; i++ {
		fake.store(bus.GeneralChannel(), uint64(i*10), alice.sign(t, bus.GeneralChannel(), "m"+strconv.Itoa(i)))
	}
	stream := &streamReads{fakeBus: fake}
	monitor := New(Config{Admin: fake, History: stream, Report: func(err error) { t.Error(err) }, CacheBytes: 4 * messageCost(Message{Text: "m1"})})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if channels := monitor.Channels(); channels[0].Messages == count {
			return monitor, stream
		}
		if time.Now().After(deadline) {
			t.Fatal("the monitor did not index the messages")
		}
	}
}

func TestOlderBodiesAreReadFromTheStreamAndThenCached(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	if channel := monitor.Channels()[0]; channel.Messages != 8 || channel.LastSequence != 80 {
		t.Fatalf("channel = %+v, want 8 messages up to sequence 80 from the index", channel)
	}

	page := messagesOf(t, monitor, "general", 0, 4) // 10 to 40, evicted by the later ones
	if len(page) != 4 || page[0].Text != "m1" || page[3].Text != "m4" || page[0].Status != Verified || page[0].From != "api-client@alice" {
		t.Fatalf("page = %+v", page)
	}
	if stream.reads.Load() == 0 {
		t.Error("the page came without a stream read although the cache holds the newest four")
	}
	reads := stream.reads.Load()
	messagesOf(t, monitor, "general", 0, 4)
	// The cache holds a whole page: a smaller one would make the result depend on the order of
	// the parallel reads, which evict each other.
	if stream.reads.Load() != reads {
		t.Errorf("the same page read %d more messages, want its tail cached", stream.reads.Load()-reads)
	}
}

func TestAMessageTheStreamNoLongerHasIsDroppedAndTheNextTakesItsPlace(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	stream.remove(20)

	page := messagesOf(t, monitor, "general", 0, 3)
	if len(page) != 3 || page[0].Sequence != 10 || page[1].Sequence != 30 || page[2].Sequence != 40 {
		t.Errorf("page = %+v, want sequences 10, 30 and 40, the gap filled from the next message", page)
	}
}

func TestAFailedStreamReadFailsThePageAndKeepsTheCache(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	server := serve(monitor)
	defer server.Close()
	failure := errors.New("bus unavailable, reconnecting")
	stream.fail.Store(&failure)

	if _, err := monitor.Messages(context.Background(), "general", 0, 4); !errors.Is(err, failure) {
		t.Errorf("error = %v, want the failed read", err)
	}
	response, body := call(t, server, "GET", "/api/v1/messages?channel=general&limit=4", "", nil)
	if response.StatusCode != http.StatusServiceUnavailable || errorCode(body) != "bus_unavailable" {
		t.Errorf("answer %d %s, want 503 bus_unavailable", response.StatusCode, body)
	}
	if newest := messagesOf(t, monitor, "general", 60, 10); len(newest) != 2 || newest[1].Text != "m8" {
		t.Errorf("the cached tail is not served while reads fail: %+v", newest)
	}

	stream.fail.Store(nil)
	if page := messagesOf(t, monitor, "general", 0, 4); len(page) != 4 {
		t.Errorf("after the broker is back: %+v", page)
	}
}

func TestSearchReadsBodiesThroughTheCache(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	query, err := ParseQuery("m2")
	if err != nil {
		t.Fatal(err)
	}
	result, err := monitor.Search(context.Background(), query, 10, "")
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Sequence != 20 || result.Hits[0].Snippet.Text != "m2" {
		t.Fatalf("search = %+v, %v", result, err)
	}
	if stream.reads.Load() == 0 {
		t.Error("the search matched an evicted body without reading it")
	}
}
