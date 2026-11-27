package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIndexDropsFromTheFrontAndByRow(t *testing.T) {
	index, messages := newIndex(), sampleMessages()
	for _, message := range messages {
		index.add(message)
	}
	if got := index.countThrough(30); got != 30 {
		t.Fatalf("%d rows through sequence 30, want 30", got)
	}

	dropped := index.dropBelow(21)
	if dropped != 20 || index.countThrough(1000) != 41 {
		t.Errorf("dropped %d rows and %d are left, want 20 and 41", dropped, index.countThrough(1000))
	}
	if _, found := index.find("general", 3); found {
		t.Error("a row below the first sequence is still there")
	}
	for _, message := range messages {
		_, known := index.lookup(message.ID)
		if message.ID != "" && known != (message.Sequence >= 21) {
			t.Errorf("#%d: by-ID lookup known = %v after dropping below 21", message.Sequence, known)
		}
	}

	index.remove("general", 30)
	if _, found := index.find("general", 30); found || index.countThrough(1000) != 40 {
		t.Error("remove left the row")
	}
	index.remove("general", 30) // again: nothing
	index.dropBelow(1000)
	if len(index.channels) != 0 || len(index.byID) != 0 {
		t.Errorf("an empty index still holds %d channels, %d IDs", len(index.channels), len(index.byID))
	}
}

func TestFollowLogDropsWhatRetentionDeleted(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	first, _ := monitor.MessageByID(context.Background(), messagesOf(t, monitor, "general", 0, 1)[0].ID)
	if first.Sequence != 10 {
		t.Fatalf("setup: %+v", first)
	}

	stream.remove(10, 20, 30) // from the front: retention
	stream.remove(60)         // from the middle: a delete by hand
	if err := monitor.followLog(context.Background()); err != nil {
		t.Fatal(err)
	}

	channel := monitor.Channels()[0]
	if channel.Messages != 4 || channel.LastSequence != 80 {
		t.Errorf("channel = %+v, want 4 messages (40, 50, 70, 80) up to 80", channel)
	}
	if page := messagesOf(t, monitor, "general", 0, 10); len(page) != 4 || page[0].Sequence != 40 || page[2].Sequence != 70 {
		t.Errorf("page = %+v, want 40, 50, 70, 80", page)
	}
	if _, err := monitor.MessageByID(context.Background(), first.ID); !errors.Is(err, ErrUnknownMessage) {
		t.Errorf("the first message by ID: %v, want ErrUnknownMessage", err)
	}
	if before, err := monitor.MessagesBefore(context.Background(), "general", 70, 10); err != nil || len(before) != 2 || before[1].Sequence != 50 {
		t.Errorf("before 70 = %+v, %v, want 40 and 50", before, err)
	}
}

func TestFollowLogKeepsEverythingWhileTheLogIsUnreadable(t *testing.T) {
	monitor, stream := lazyMonitor(t, 8)
	failure := errors.New("bus unavailable, reconnecting")
	stream.fail.Store(&failure)
	stream.remove(10)
	// A page read fails as a whole, and the failed read drops nothing.
	if _, err := monitor.Messages(context.Background(), "general", 0, 3); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the failed read", err)
	}
	if channel := monitor.Channels()[0]; channel.Messages != 8 {
		t.Errorf("channel = %+v, want all 8 messages kept", channel)
	}
}

func TestAReadThatFindsAMessageGoneHastensTheLookAtTheLog(t *testing.T) {
	old := retentionInterval
	retentionInterval = time.Hour
	t.Cleanup(func() { retentionInterval = old }) // after the monitor, which lazyMonitor stops with the test
	monitor, stream := lazyMonitor(t, 8)
	stream.remove(10, 20, 30, 40, 50, 60) // everything the cache does not hold

	// The first row is found gone by a read; the rest follow from the log's first sequence without
	// waiting for the interval.
	messagesOf(t, monitor, "general", 0, 1)
	for deadline := time.Now().Add(5 * time.Second); monitor.Channels()[0].Messages > 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("channel = %+v, want only the 2 messages the log still holds", monitor.Channels()[0])
		}
	}
}
