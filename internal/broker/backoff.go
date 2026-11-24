package broker

import (
	"math/rand/v2"
	"time"
)

// Backoff is the schedule of a connection's attempts to reach the broker again: Initial for the
// first, doubling for each failed one, never more than Max.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
}

// DefaultBackoff is 1 s doubling up to 30 s: quick after a broker restart, gentle when the broker
// stays away.
var DefaultBackoff = Backoff{Initial: time.Second, Max: 30 * time.Second}

// Delay is the wait before the given attempt, counted from 1 for the first attempt after the
// connection was lost. It depends on the attempt alone: nats.go counts again from 1 after each
// successful connect. The result lies between half of the schedule's value and all of it, so that
// connections that lost the broker together do not come back together.
func (backoff Backoff) Delay(attempt int) time.Duration {
	delay := backoff.Initial
	for step := 1; step < attempt && delay < backoff.Max; step++ {
		delay *= 2
	}
	delay = min(delay, backoff.Max)
	return delay/2 + rand.N(delay/2+1)
}
