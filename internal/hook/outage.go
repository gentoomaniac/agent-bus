package hook

import (
	"fmt"
	"sync"
	"time"
)

// Variables only so that the tests need not wait for them.
var (
	// outageGrace is how long the bus must have been out of reach before a hook says so: a broker
	// that restarts is back within seconds, and the agent needs no notice for that.
	outageGrace = time.Minute
	// outagePoll is how often a waiting waiter looks at the bus, since nothing else wakes it for an outage.
	outagePoll = 5 * time.Second
)

// outage remembers when a hook or the waiter first saw the bus out of reach, so that each outage
// is reported once. The adapter reconnects by itself and the mail waits at the broker, so the
// report only explains why nothing arrives meanwhile.
type outage struct {
	mu       sync.Mutex
	since    time.Time // zero while the bus is reachable
	reported bool
}

// report takes the adapter's view of the bus (nil when reachable) and returns the sentence that tells
// the agent, once the outage has lasted outageGrace and not been reported yet; otherwise "".
func (o *outage) report(busErr error, now time.Time) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if busErr == nil {
		o.since, o.reported = time.Time{}, false
		return ""
	}
	if o.since.IsZero() {
		o.since = now
	}
	if o.reported || now.Sub(o.since) < outageGrace {
		return ""
	}
	o.reported = true
	return fmt.Sprintf("the bus has been out of reach for %s (%v). The MCP server reconnects by itself and no mail is lost; "+
		"new mail will be reported when it is back.", now.Sub(o.since).Round(time.Second), busErr)
}
