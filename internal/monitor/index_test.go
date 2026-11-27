package monitor

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// sampleMessages makes messages over a few channels, shuffled as concurrent followers deliver them.
func sampleMessages() []Message {
	var messages []Message
	channels := []string{"general", "group:build", "dm:a@x:b@y"}
	for sequence := uint64(1); sequence <= 60; sequence++ {
		moment := time.UnixMilli(1_700_000_000_000 + int64(sequence)).UTC()
		message := Message{
			Sequence: sequence, Channel: channels[sequence%3], Status: verifications[sequence%3],
			ID: envelope.NewID(moment), From: "a@x", To: "general", Type: "message",
			Thread: fmt.Sprintf("thread-%d", sequence%7), Time: moment, Text: "body", Title: "title",
		}
		if sequence%4 == 0 {
			message.Targets = []string{"b@y", "c@z"}
		}
		messages = append(messages, message)
	}
	messages = append(messages, Message{Sequence: 61, Channel: "general", Status: Rejected, Reason: "not json"})
	rand.Shuffle(len(messages), func(i, j int) { messages[i], messages[j] = messages[j], messages[i] })
	return messages
}

func TestIndexDescribesTheMessagesItWasGiven(t *testing.T) {
	index := newIndex()
	given := map[string][]Message{}
	for _, message := range sampleMessages() {
		index.add(message)
		if index.add(message) {
			t.Fatalf("#%d was added twice", message.Sequence)
		}
		given[message.Channel] = append(given[message.Channel], message)
	}

	for channel, messages := range given {
		slices.SortFunc(messages, func(a, b Message) int { return cmp.Compare(a.Sequence, b.Sequence) })
		described := index.describeAll(channel, index.channels[channel])
		if len(described) != len(messages) {
			t.Fatalf("%s: %d rows for %d messages", channel, len(described), len(messages))
		}
		for i, message := range messages {
			// The index keeps no body, no reply and no reason.
			want := message
			want.Title, want.Text, want.Refs, want.ReplyTo, want.Reason = "", "", nil, nil, ""
			got := described[i]
			if !got.Time.Equal(want.Time) {
				t.Errorf("%s #%d: time %v, want %v", channel, message.Sequence, got.Time, want.Time)
			}
			got.Time, want.Time = time.Time{}, time.Time{}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s #%d: described as %+v, want %+v", channel, message.Sequence, got, want)
			}
			if message.ID == "" {
				continue
			}
			if at, known := index.lookup(message.ID); !known || at != (position{channel, message.Sequence}) {
				t.Errorf("ID %s is at %+v (%v), want %s #%d", message.ID, at, known, channel, message.Sequence)
			}
		}
	}
}

func TestIndexFindsARowBySequence(t *testing.T) {
	index := newIndex()
	for _, message := range sampleMessages() {
		index.add(message)
	}
	if entry, found := index.find("group:build", 4); !found || entry.sequence != 4 {
		t.Errorf("find(group:build, 4) = %+v, %v", entry, found)
	}
	if _, found := index.find("group:build", 5); found {
		t.Error("found a row of another channel")
	}
	if _, known := index.lookup("not a ULID"); known {
		t.Error("looked up an ID that is no ULID")
	}
}
