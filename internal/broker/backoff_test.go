package broker

import (
	"testing"
	"time"
)

func TestBackoffDelayDoublesFromInitialAndStopsAtMax(t *testing.T) {
	backoff := Backoff{Initial: time.Second, Max: 30 * time.Second}
	// The schedule's value for each attempt; the delay is in [value/2, value].
	schedule := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for attempt, seconds := range schedule {
		value := seconds * time.Second
		for range 200 {
			delay := backoff.Delay(attempt + 1)
			if delay < value/2 || delay > value {
				t.Fatalf("attempt %d: delay %s, want between %s and %s", attempt+1, delay, value/2, value)
			}
		}
	}
	if delay := backoff.Delay(1_000_000); delay > backoff.Max {
		t.Fatalf("a huge attempt number gave %s, over the cap %s", delay, backoff.Max)
	}
}

func TestBackoffDelayIsJittered(t *testing.T) {
	backoff := DefaultBackoff
	seen := map[time.Duration]bool{}
	for range 100 {
		seen[backoff.Delay(3)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("100 delays for the same attempt took only %d different values", len(seen))
	}
}

// After a successful connect nats.go counts attempts from 1 again, so the schedule starts over.
func TestBackoffDelayStartsOverAtAttemptOne(t *testing.T) {
	backoff := DefaultBackoff
	if delay := backoff.Delay(1); delay > backoff.Initial {
		t.Fatalf("the first attempt waits %s, want at most %s", delay, backoff.Initial)
	}
}
