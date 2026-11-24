package monitor

import "sync"

// hub fans values out to subscribers. A subscriber that cannot keep up is closed rather than
// waited for: its client reconnects and reads what it missed.
type hub[T any] struct {
	mu          sync.Mutex
	subscribers map[chan T]struct{}
}

func (h *hub[T]) publish(value T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		select {
		case subscriber <- value:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
}

// subscribe returns the values published from now on. The channel is closed when the subscriber
// falls behind; unsubscribe is safe to call then.
func (h *hub[T]) subscribe() (values <-chan T, unsubscribe func()) {
	subscriber := make(chan T, subscriberBuffer)
	h.mu.Lock()
	if h.subscribers == nil {
		h.subscribers = map[chan T]struct{}{}
	}
	h.subscribers[subscriber] = struct{}{}
	h.mu.Unlock()
	return subscriber, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, subscribed := h.subscribers[subscriber]; subscribed {
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
}
