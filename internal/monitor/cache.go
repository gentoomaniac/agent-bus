package monitor

import (
	"container/list"
	"context"
	"sync"
)

// DefaultCacheBytes is the size of the body cache when the Config names none: about 60 000
// messages of 1 KiB.
const DefaultCacheBytes = 64 << 20

// bodyCache keeps recently used messages with their bodies, by stream sequence, up to a size in
// bytes, least recently used out first. The size of a message is an estimate (messageCost), not a
// measurement of the heap.
type bodyCache struct {
	limit int64

	mu      sync.Mutex
	size    int64
	order   *list.List // of *cached, most recently used first
	items   map[uint64]*list.Element
	loading map[uint64]*load
}

type cached struct {
	sequence uint64
	message  Message
	cost     int64
}

// load is a read of one missing message that every request for it waits for.
type load struct {
	done    chan struct{}
	message Message
	err     error
}

func newBodyCache(limit int64) *bodyCache {
	return &bodyCache{limit: limit, order: list.New(), items: map[uint64]*list.Element{}, loading: map[uint64]*load{}}
}

// messageCost estimates what a cached message holds: its text and a fixed share for the struct,
// the strings' headers and the metadata.
func messageCost(message Message) int64 {
	cost := int64(512 + len(message.Title) + len(message.Text) + len(message.Reason))
	for _, ref := range message.Refs {
		cost += int64(len(ref))
	}
	return cost
}

// put stores a message, which makes it the most recently used one, and drops the least recently
// used until the cache is within its limit. A message larger than the limit is not kept.
func (cache *bodyCache) put(message Message) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.putLocked(message)
}

func (cache *bodyCache) putLocked(message Message) {
	cost := messageCost(message)
	if element, found := cache.items[message.Sequence]; found {
		cache.remove(element)
	}
	if cost > cache.limit {
		return
	}
	cache.items[message.Sequence] = cache.order.PushFront(&cached{message.Sequence, message, cost})
	cache.size += cost
	for cache.size > cache.limit {
		cache.remove(cache.order.Back())
	}
}

func (cache *bodyCache) remove(element *list.Element) {
	entry := element.Value.(*cached)
	cache.size -= entry.cost
	cache.order.Remove(element)
	delete(cache.items, entry.sequence)
}

// forget drops the message of a sequence the stream no longer holds.
func (cache *bodyCache) forget(sequence uint64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, found := cache.items[sequence]; found {
		cache.remove(element)
	}
}

// forgetBelow drops the messages with a sequence below the first one.
func (cache *bodyCache) forgetBelow(first uint64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for sequence, element := range cache.items {
		if sequence < first {
			cache.remove(element)
		}
	}
}

// get returns the message from the cache, or reads it with read and keeps it. Requests for the
// same sequence at the same time share one read. A failed read keeps and evicts nothing.
func (cache *bodyCache) get(ctx context.Context, sequence uint64, read func(context.Context) (Message, error)) (Message, error) {
	return cache.fetch(ctx, sequence, read, true)
}

// getWithoutKeeping is get for a reader that touches many messages once, a search: what it reads is
// not kept and what it finds cached is not made more recently used, so it cannot push out the
// messages clients are looking at.
func (cache *bodyCache) getWithoutKeeping(ctx context.Context, sequence uint64, read func(context.Context) (Message, error)) (Message, error) {
	return cache.fetch(ctx, sequence, read, false)
}

func (cache *bodyCache) fetch(ctx context.Context, sequence uint64, read func(context.Context) (Message, error), keep bool) (Message, error) {
	cache.mu.Lock()
	if element, found := cache.items[sequence]; found {
		if keep {
			cache.order.MoveToFront(element)
		}
		message := element.Value.(*cached).message
		cache.mu.Unlock()
		return message, nil
	}
	pending, shared := cache.loading[sequence]
	if !shared {
		pending = &load{done: make(chan struct{})}
		cache.loading[sequence] = pending
	}
	cache.mu.Unlock()

	if shared {
		select {
		case <-pending.done:
			return pending.message, pending.err
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
	// The read outlives this request if it is cancelled, since others may be waiting on it.
	pending.message, pending.err = read(context.WithoutCancel(ctx))
	cache.mu.Lock()
	delete(cache.loading, sequence)
	if pending.err == nil && keep {
		cache.putLocked(pending.message)
	}
	cache.mu.Unlock()
	close(pending.done)
	return pending.message, pending.err
}
