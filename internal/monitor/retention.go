package monitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// retentionInterval is how often the monitor compares its index with the log
// (docs/design/lazy-monitor.md). A variable for the tests.
var retentionInterval = 30 * time.Second

// followRetention keeps the index to what the log still holds, until ctx ends: at every interval,
// and at once when a read found a message gone.
func (monitor *Monitor) followRetention(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retentionInterval):
		case <-monitor.retentionDue:
		}
		if err := monitor.followLog(ctx); err != nil && ctx.Err() == nil {
			monitor.report(err)
		}
	}
}

// followLog drops from the index and the cache what the log no longer holds. Retention deletes
// from the front, so the log's first sequence is the boundary; a record deleted in the middle (a
// purge by hand) shows as a log with fewer records than the index has, and is looked for with one
// read per row.
func (monitor *Monitor) followLog(ctx context.Context) error {
	state, err := monitor.config.History.LogState(ctx)
	if err != nil {
		return err
	}
	monitor.mu.Lock()
	monitor.index.dropBelow(state.FirstSequence)
	held := monitor.index.countThrough(state.LastSequence)
	monitor.mu.Unlock()
	monitor.bodies.forgetBelow(state.FirstSequence)

	// The log may hold more records than the index has rows (subjects the monitor does not follow,
	// records not read yet), never fewer unless something was deleted.
	if uint64(held) <= state.Messages {
		return nil
	}
	return monitor.dropMissing(ctx, state.LastSequence)
}

// dropMissing asks the log for every record of the index up to the sequence, without keeping the
// bodies, and drops the rows it no longer has.
func (monitor *Monitor) dropMissing(ctx context.Context, last uint64) error {
	var candidates []Message
	monitor.mu.Lock()
	for channel, rows := range monitor.index.channels {
		for _, entry := range rows {
			if entry.sequence <= last {
				candidates = append(candidates, Message{Channel: channel, Sequence: entry.sequence})
			}
		}
	}
	monitor.mu.Unlock()

	var (
		mu      sync.Mutex
		gone    []Message
		failure error
		reads   sync.WaitGroup
	)
	limiter := make(chan struct{}, maxConcurrentReads)
	for _, candidate := range candidates {
		limiter <- struct{}{}
		reads.Go(func() {
			defer func() { <-limiter }()
			_, err := monitor.config.History.Message(ctx, candidate.Sequence)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, bus.ErrMessageGone) {
				gone = append(gone, candidate)
			} else if err != nil {
				failure = err
			}
		})
	}
	reads.Wait()
	monitor.drop(gone)
	return failure
}
