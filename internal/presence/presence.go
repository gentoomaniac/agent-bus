// Package presence is what the bus knows about whether an agent can respond and what it is doing
// (design.md §7, Presence). The harness's hooks report events; a Tracker turns them into a Record;
// a Publisher puts the Record on the bus for the owner's monitor and for list_agents. Nothing
// here costs the model a token.
package presence

import (
	"strings"
	"time"
)

// The states of a Record. error:<kind> is Error plus the kind the harness reported.
const (
	Working         = "working"
	Idle            = "idle"
	WaitingForOwner = "waiting_for_owner"
	RateLimited     = "rate_limited"
	Error           = "error:"
	Unresponsive    = "unresponsive"
	Offline         = "offline"
)

const (
	// MinPublishInterval is the least time between two publishes caused by changes.
	MinPublishInterval = 3 * time.Second
	// HeartbeatInterval is how often an unchanged record is published again.
	HeartbeatInterval = 30 * time.Second
	// OfflineAfter is how long a reader waits for a heartbeat before it calls the agent offline.
	OfflineAfter = 90 * time.Second
	// UnresponsiveAfter is how long targeted mail may wait unseen at an idle agent.
	UnresponsiveAfter = 10 * time.Minute

	maxRecent    = 10
	maxWorkers   = 10
	maxTextRunes = 60 // activity and worker descriptions are for a glance, never content
)

// Event kinds: what the harness's hooks report.
const (
	EventPrompt       = "prompt"        // the user sent a prompt: the model works
	EventStop         = "stop"          // the turn ended
	EventTool         = "tool"          // a tool starts: Tool and Detail say which
	EventToolDone     = "tool-done"     // a tool ended
	EventWorkerStart  = "worker-start"  // a subagent starts: Detail is its description
	EventWorkerEnd    = "worker-end"    // a subagent ended
	EventFailure      = "failure"       // the turn ended on an error: Detail is the kind
	EventNeedsOwner   = "needs-owner"   // the harness waits for the human
	EventSessionStart = "session-start" // a session begins: idle, nothing running
	EventSessionEnd   = "session-end"   // the session is over
)

// Event is one report of a hook, already reduced to what presence needs.
type Event struct {
	Kind   string `json:"kind"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Worker is a subagent that is running.
type Worker struct {
	Description string    `json:"description"`
	Since       time.Time `json:"since"`
}

// Entry is one item of the recent activity.
type Entry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Record is the presence of one agent as it is published.
type Record struct {
	State         string    `json:"state"`
	Since         time.Time `json:"since"`
	Updated       time.Time `json:"updated"` // of the last publish: the heartbeat
	Activity      string    `json:"activity,omitempty"`
	ActivitySince time.Time `json:"activity_since,omitzero"`
	Workers       []Worker  `json:"workers,omitempty"`
	Recent        []Entry   `json:"recent,omitempty"`  // oldest first
	Pending       int       `json:"pending,omitempty"` // targeted mail the agent has not marked read
}

// StateAt is the state a reader shows: offline when the record says so or its last heartbeat is
// more than offlineAfter ago (OfflineAfter unless a reader chooses otherwise).
func (record Record) StateAt(now time.Time, offlineAfter time.Duration) string {
	if record.State == Offline || now.Sub(record.Updated) > offlineAfter {
		return Offline
	}
	return record.State
}

// Mail is what the adapter knows about targeted mail that waits for the agent.
type Mail struct {
	Pending      int       // unread, targeted
	OldestUnseen time.Time // arrival of the oldest that no tool call has listed yet; zero if none
}

// Tracker follows the events of one session.
type Tracker struct {
	state         string
	since         time.Time
	activity      string
	activitySince time.Time
	workers       []Worker
	recent        []Entry
}

// NewTracker starts idle: the session is waiting for its first prompt.
func NewTracker(now time.Time) *Tracker { return &Tracker{state: Idle, since: now} }

// Apply changes the state for one event. Unknown kinds are ignored.
func (tracker *Tracker) Apply(event Event, now time.Time) {
	switch event.Kind {
	case EventPrompt:
		tracker.enter(Working, now)
		tracker.activity = ""
	case EventStop:
		tracker.enter(Idle, now)
		tracker.activity = ""
	case EventTool:
		tracker.enter(Working, now)
		tracker.activity = clip(strings.TrimSpace(event.Tool + " " + event.Detail))
		tracker.activitySince = now
		tracker.note(tracker.activity, now)
	case EventToolDone:
		tracker.enter(Working, now)
		tracker.activity = ""
	case EventWorkerStart:
		tracker.enter(Working, now)
		tracker.workers = append(tracker.workers, Worker{Description: clip(event.Detail), Since: now})
		if len(tracker.workers) > maxWorkers {
			tracker.workers = tracker.workers[1:]
		}
		tracker.note("worker: "+clip(event.Detail), now)
	case EventWorkerEnd:
		// Subagents are not told apart: the oldest one is taken to be the one that ended.
		if len(tracker.workers) > 0 {
			tracker.note("worker done: "+tracker.workers[0].Description, now)
			tracker.workers = tracker.workers[1:]
		}
	case EventFailure:
		kind := errorKind(event.Detail)
		state := Error + kind
		if kind == "rate_limit" {
			state = RateLimited
		}
		tracker.enter(state, now)
		tracker.activity = ""
		tracker.note(state, now)
	case EventNeedsOwner:
		tracker.enter(WaitingForOwner, now)
		tracker.activity = ""
		tracker.note(WaitingForOwner, now)
	case EventSessionStart:
		tracker.enter(Idle, now)
		tracker.clearRunning()
	case EventSessionEnd:
		tracker.enter(Offline, now)
		tracker.clearRunning()
		tracker.note(Offline, now)
	}
}

func (tracker *Tracker) enter(state string, now time.Time) {
	if state != tracker.state {
		tracker.state, tracker.since = state, now
	}
}

func (tracker *Tracker) clearRunning() {
	tracker.activity = ""
	tracker.workers = nil
}

func (tracker *Tracker) note(text string, now time.Time) {
	tracker.recent = append(tracker.recent, Entry{At: now, Text: text})
	if len(tracker.recent) > maxRecent {
		tracker.recent = tracker.recent[1:]
	}
}

// Record is the presence to publish at the given time. An idle agent that has left targeted mail
// unseen for UnresponsiveAfter is unresponsive: it does not react, whatever its harness reports.
func (tracker *Tracker) Record(now time.Time, mail Mail) Record {
	record := Record{
		State: tracker.state, Since: tracker.since, Updated: now, Activity: tracker.activity,
		Workers: append([]Worker(nil), tracker.workers...), Recent: append([]Entry(nil), tracker.recent...),
		Pending: mail.Pending,
	}
	if tracker.activity != "" {
		record.ActivitySince = tracker.activitySince
	}
	if overdue := mail.OldestUnseen.Add(UnresponsiveAfter); tracker.state == Idle && !mail.OldestUnseen.IsZero() && !now.Before(overdue) {
		record.State, record.Since = Unresponsive, overdue
	}
	return record
}

// clip cuts text to what fits a glance, on a rune boundary, with newlines flattened.
func clip(text string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > maxTextRunes {
		return string(runes[:maxTextRunes-1]) + "…"
	}
	return string(runes)
}

// errorKind keeps the kind a harness reports to lowercase letters, digits and underscores, so it
// is safe inside a state name.
func errorKind(kind string) string {
	kind = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return -1
	}, strings.ToLower(kind))
	if kind == "" {
		return "unknown"
	}
	return kind[:min(len(kind), 30)]
}
