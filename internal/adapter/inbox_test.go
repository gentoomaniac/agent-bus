package adapter

import (
	"context"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

func buffered(id string, size int, inProgress *int) *Message {
	return &Message{
		Envelope:  envelope.Envelope{ID: id, From: "a@o"},
		TargetsMe: true,
		Channel:   bus.GeneralChannel(),
		size:      size,
		delivery:  bus.Delivery{InProgress: func(context.Context) error { *inProgress++; return nil }},
	}
}

func TestInboxFetchBatchIsBoundedByCountAndBytes(t *testing.T) {
	buffer := &inbox{maxMessages: 500, maxBytes: 8 << 20}
	// 8 MB over 96 KB records is 85 records, fewer than the 500 messages.
	if got := buffer.fetchBatch(); got != 85 {
		t.Errorf("empty buffer batch = %d, want 85", got)
	}
	small := &inbox{maxMessages: 3, maxBytes: 8 << 20}
	var calls int
	small.add(buffered("1", 100, &calls))
	if got := small.fetchBatch(); got != 2 {
		t.Errorf("batch with 2 free slots = %d", got)
	}
	small.add(buffered("2", 100, &calls))
	small.add(buffered("3", 100, &calls))
	if got := small.fetchBatch(); got != 0 {
		t.Errorf("full buffer batch = %d, want 0", got)
	}
	heavy := &inbox{maxMessages: 500, maxBytes: 8 << 20}
	heavy.add(buffered("big", 8<<20, &calls))
	if got := heavy.fetchBatch(); got != 0 {
		t.Errorf("byte-full buffer batch = %d, want 0", got)
	}
}

func TestInboxAddFindRemoveAndKeepAlive(t *testing.T) {
	buffer := &inbox{maxMessages: 10, maxBytes: 1 << 20}
	var inProgress int
	first, second := buffered("1", 10, &inProgress), buffered("2", 20, &inProgress)
	buffer.add(first)
	buffer.add(second)
	if buffer.find(bus.GeneralChannel(), "a@o", "2") != second || buffer.find(bus.GeneralChannel(), "b@o", "2") != nil || buffer.find(bus.DMChannel("x@o"), "a@o", "2") != nil {
		t.Error("find does not match on destination, sender and ID")
	}
	if buffer.findByID("1") != first {
		t.Error("findByID")
	}
	if err := buffer.keepAlive(context.Background()); err != nil || inProgress != 2 {
		t.Errorf("keepAlive: %d messages told, err %v", inProgress, err)
	}
	buffer.remove(first)
	if buffer.bytes != 20 || len(buffer.messages) != 1 || buffer.messages[0] != second {
		t.Errorf("after remove: %d bytes, %d messages", buffer.bytes, len(buffer.messages))
	}
}
