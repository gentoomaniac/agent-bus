//go:build integration

package itest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
)

// TestMonitorFollowsRetention lets the log's size limit delete the oldest messages and checks that
// the monitor stops listing them: a page read that finds one gone drops it, and the look at the
// log that follows drops the rest.
func TestMonitorFollowsRetention(t *testing.T) {
	h := startBus(t)
	aliceSession, _ := h.startAdapter(t, alice)
	ctx := timeout(t)
	for i := range 3 {
		if _, err := aliceSession.Publish(ctx, "general", sayTo(fmt.Sprintf("early %d", i), envelope.TargetAll)); err != nil {
			t.Fatal(err)
		}
	}

	monitorAdmin, monitorHistory := h.monitorReads(t)
	// A cache that keeps nothing, so that a page read has to ask the log; a cached message would be
	// dropped only at the monitor's next look at the log, 30 seconds on.
	web := monitor.New(monitor.Config{Admin: monitorAdmin, History: monitorHistory, CacheBytes: 1, Report: func(err error) { t.Errorf("monitor: %v", err) }})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { web.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	server := serveMonitor(t, web)
	early := waitForMonitorMessages(t, server, "general", 3)

	h.run(t, h.agentBusctl, "--direct", "init", "--max-bytes", "4096")
	for i := range 4 {
		if _, err := aliceSession.Publish(ctx, "general", sayTo(fmt.Sprintf("filler %d %s", i, strings.Repeat("x", 1500)), envelope.TargetAll)); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := h.rawAdmin(t, broker.BusAdminUser).js.Stream(ctx, "BUS")
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.FirstSeq <= early[len(early)-1].Sequence {
		t.Fatalf("the oldest record, %d, is still in the log, setup did not evict the early messages", info.State.FirstSeq)
	}

	general := func() monitor.Channel { return getJSON[[]monitor.Channel](t, server, "/api/v1/channels")[0] }
	waitFor := func(what string, done func(monitor.Channel) bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !done(general()); time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: general lists %d messages up to %d, the log holds %d up to %d", what, general().Messages, general().LastSequence, info.State.Msgs, info.State.LastSeq)
			}
		}
	}
	waitFor("the monitor reading the newest message", func(c monitor.Channel) bool { return c.LastSequence == info.State.LastSeq })
	getJSON[[]monitor.Message](t, server, "/api/v1/messages?channel=general") // finds the first gone one
	waitFor("the monitor dropping what the log deleted", func(c monitor.Channel) bool { return c.Messages == int(info.State.Msgs) })
	if page := getJSON[[]monitor.Message](t, server, "/api/v1/messages?channel=general"); page[0].Sequence < info.State.FirstSeq {
		t.Errorf("the first message of the page, %d, is below the log's first, %d", page[0].Sequence, info.State.FirstSeq)
	}
	response, err := http.Get(server.URL + "/api/v1/messages/by-id?id=" + early[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("by-id of a deleted message: status %d, want 404", response.StatusCode)
	}
}
