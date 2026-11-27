package monitor

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// maxHeapPerMessage is the bound the lazy monitor keeps (it measures about 235 bytes per message,
// docs/design/lazy-monitor.md; the monitor held about 2100 before the index).
const maxHeapPerMessage = 300

// TestHeapPerMessage fills a monitor with messages of about 1 KiB and checks that what it keeps
// per message, beyond the small body cache, stays an index row. Every message has a thread of its
// own, the worst case for the name table.
func TestHeapPerMessage(t *testing.T) {
	const count = 100_000
	heap := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	monitor := New(Config{CacheBytes: 64 << 10})
	moment := time.Now().UTC()

	before := heap()
	for i := range count {
		monitor.add(Message{
			Sequence: uint64(i + 1), Channel: fmt.Sprintf("group:g%d", i%50), Status: Verified,
			ID: envelope.NewID(moment), From: fmt.Sprintf("agent-%d@owner", i%40), To: fmt.Sprintf("g%d", i%50), Type: "message",
			Targets: []string{fmt.Sprintf("agent-%d@owner", (i+1)%40)}, Thread: envelope.NewID(moment), Time: moment,
			Title: "title", Text: strings.Repeat("x", 1000) + fmt.Sprint(i),
		})
	}
	perMessage := (int64(heap()) - int64(before)) / count
	t.Logf("%d bytes of heap per message", perMessage)
	if perMessage > maxHeapPerMessage {
		t.Errorf("the monitor keeps %d bytes per message, want at most %d", perMessage, maxHeapPerMessage)
	}
	runtime.KeepAlive(monitor)
}
