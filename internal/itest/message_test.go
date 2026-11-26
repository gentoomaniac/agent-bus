//go:build integration

package itest

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// TestReadMessageBySequence reads records of any channel by stream sequence with the monitor's own
// login, many at a time as a page does, and gets ErrMessageGone for a sequence the log does not hold.
func TestReadMessageBySequence(t *testing.T) {
	h := startBus(t)
	ctx := timeout(t)
	publisher := h.transport(t, alice)

	channels := []bus.Channel{bus.GeneralChannel(), bus.DMChannel(bob)}
	payloads := map[uint64][]byte{}
	for i := range 30 {
		payload := []byte(fmt.Sprintf("record %d", i))
		sequence, err := publisher.Publish(ctx, channels[i%len(channels)], fmt.Sprintf("m%d", i), payload)
		if err != nil {
			t.Fatal(err)
		}
		payloads[sequence] = payload
	}

	_, history := h.monitorReads(t)
	var wg sync.WaitGroup
	for sequence, payload := range payloads {
		wg.Go(func() {
			record, err := history.Message(ctx, sequence)
			if err != nil || record.Sequence != sequence || !bytes.Equal(record.Payload, payload) {
				t.Errorf("Message(%d) = %q, %v, want %q", sequence, record.Payload, err, payload)
			}
		})
	}
	wg.Wait()

	if _, err := history.Message(ctx, 1_000_000); !errors.Is(err, bus.ErrMessageGone) {
		t.Errorf("a sequence the log never used gave %v, want ErrMessageGone", err)
	}
}
