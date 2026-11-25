//go:build integration

package itest

import (
	"fmt"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func TestRetention(t *testing.T) {
	h := startBus(t)
	ctx := timeout(t)
	busAdmin := h.rawAdmin(t, broker.BusAdminUser)
	streamInfo := func() (maxAge time.Duration, maxBytes int64, firstSequence uint64) {
		stream, err := busAdmin.js.Stream(ctx, "BUS")
		if err != nil {
			t.Fatal(err)
		}
		info, err := stream.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return info.Config.MaxAge, info.Config.MaxBytes, info.State.FirstSeq
	}

	if maxAge, maxBytes, _ := streamInfo(); maxAge != 90*24*time.Hour || maxBytes != 1<<30 {
		t.Errorf("BUS keeps %s and %d bytes, want 90 days and 1 GiB", maxAge, maxBytes)
	}

	// An agent that was offline while the log expired has a cursor that points at messages that
	// are gone; it reads on from the oldest one left.
	bobTransport, aliceTransport := h.transport(t, bob), h.transport(t, alice)
	expired, err := bobTransport.Publish(ctx, bus.DMChannel(alice), "expired", []byte("will be gone"))
	if err != nil {
		t.Fatal(err)
	}
	h.run(t, h.agentBusctl, "--direct", "init", "--max-bytes", "4096")
	if _, maxBytes, _ := streamInfo(); maxBytes != 4096 {
		t.Fatalf("after init --max-bytes 4096 the existing stream keeps %d bytes", maxBytes)
	}
	filler := make([]byte, 1500)
	for i := 0; i < 3; i++ {
		if _, err := bobTransport.Publish(ctx, bus.GeneralChannel(), fmt.Sprintf("filler-%d", i), filler); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, first := streamInfo(); first <= expired {
		t.Fatalf("the oldest record, %d, is still in the log", first)
	}
	fresh, err := bobTransport.Publish(ctx, bus.DMChannel(alice), "fresh", []byte("still there"))
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := aliceTransport.Fetch(ctx, bus.DMChannel(alice), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Sequence != fresh {
		t.Fatalf("alice fetched %v (%v), want only the record %d that is left", deliveries, err, fresh)
	}
}
