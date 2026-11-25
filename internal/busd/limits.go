package busd

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxCallerLimiters bounds the limiters kept, one per agent that asked; past it they start over.
const maxCallerLimiters = 10000

// callerLimits bound the requests of one kind busd answers for agents: each agent on its own, and
// all of them together beneath that, so that one agent cannot use up the others' share.
type callerLimits struct {
	all         *rate.Limiter
	callerEvery time.Duration
	callerBurst int

	mu      sync.Mutex
	callers map[string]*rate.Limiter // by agent ID
}

func newCallerLimits(allEvery time.Duration, allBurst int, callerEvery time.Duration, callerBurst int) *callerLimits {
	return &callerLimits{all: rate.NewLimiter(rate.Every(allEvery), allBurst), callerEvery: callerEvery, callerBurst: callerBurst}
}

// allow takes a request of caller from its own limit and then from the overall one.
func (limits *callerLimits) allow(caller string) bool {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	limiter := limits.callers[caller]
	if limiter == nil {
		if limits.callers == nil || len(limits.callers) >= maxCallerLimiters {
			limits.callers = map[string]*rate.Limiter{}
		}
		limiter = rate.NewLimiter(rate.Every(limits.callerEvery), limits.callerBurst)
		limits.callers[caller] = limiter
	}
	return limiter.Allow() && limits.all.Allow()
}
