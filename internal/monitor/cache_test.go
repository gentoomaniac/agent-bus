package monitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func cachedMessage(sequence uint64, text string) Message {
	return Message{Sequence: sequence, Text: text}
}

func TestCacheDropsTheLeastRecentlyUsedWhenFull(t *testing.T) {
	cost := messageCost(cachedMessage(1, "x"))
	cache := newBodyCache(3 * cost)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		cache.put(cachedMessage(sequence, "x"))
	}
	read := func(sequence uint64) (reads int) {
		_, err := cache.get(context.Background(), sequence, func(context.Context) (Message, error) {
			reads++
			return cachedMessage(sequence, "x"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return reads
	}
	if read(1) != 0 { // 1 is now the most recently used
		t.Error("1 was not in the cache")
	}
	cache.put(cachedMessage(4, "x")) // pushes out 2, the least recently used
	if read(2) != 1 {
		t.Error("2 should have been dropped")
	}
	if read(1) != 0 || read(4) != 0 {
		t.Error("the recently used ones should have stayed")
	}
}

func TestCacheDoesNotKeepAMessageLargerThanItself(t *testing.T) {
	cache := newBodyCache(1000)
	cache.put(cachedMessage(1, strings.Repeat("x", 5000)))
	if len(cache.items) != 0 || cache.size != 0 {
		t.Errorf("kept %d messages, %d bytes", len(cache.items), cache.size)
	}
}

func TestCacheSharesOneReadBetweenRequestsForTheSameMessage(t *testing.T) {
	cache := newBodyCache(1 << 20)
	var reads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	read := func(context.Context) (Message, error) {
		if reads.Add(1) == 1 {
			close(started)
		}
		<-release
		return cachedMessage(7, "once"), nil
	}

	var requests sync.WaitGroup
	for range 5 {
		requests.Go(func() {
			message, err := cache.get(context.Background(), 7, read)
			if err != nil || message.Text != "once" {
				t.Errorf("get = %+v, %v", message, err)
			}
		})
	}
	<-started
	close(release)
	requests.Wait()
	if reads.Load() != 1 {
		t.Errorf("%d reads for one message, want 1", reads.Load())
	}
}

func TestCacheKeepsNothingFromAFailedRead(t *testing.T) {
	cache := newBodyCache(1 << 20)
	failure := errors.New("broker down")
	if _, err := cache.get(context.Background(), 1, func(context.Context) (Message, error) { return Message{}, failure }); !errors.Is(err, failure) {
		t.Fatalf("error %v, want %v", err, failure)
	}
	message, err := cache.get(context.Background(), 1, func(context.Context) (Message, error) { return cachedMessage(1, "now"), nil })
	if err != nil || message.Text != "now" {
		t.Errorf("a read after the failure gave %+v, %v", message, err)
	}
}
