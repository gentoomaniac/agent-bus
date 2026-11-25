// Package presence is what the bus knows about whether an agent can respond and what it is doing
// (docs/design/presence.md). The harness's hooks report events; a Tracker turns them into a Record;
// a Publisher puts the Record on the bus for the owner's monitor and for list_agents. Nothing
// here costs the model a token.
package presence

import (
	"slices"
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
	// WorkingStaleAfter is how long a working session may stay silent before it is shown idle:
	// Claude Code runs no Stop hook when the user interrupts a turn.
	WorkingStaleAfter = 10 * time.Minute
	// WorkerStaleAfter is how long a worker may run without its stop before it is dropped.
	WorkerStaleAfter = 60 * time.Minute

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
	EventAgentCall    = "agent-call"    // the tool that launches a subagent is called: Detail is its description
	EventWorkerStart  = "worker-start"  // a subagent started: AgentID, and Detail as a fallback description
	EventWorkerEnd    = "worker-end"    // the subagent AgentID ended
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
	// AgentID identifies a subagent in worker events.
	AgentID string `json:"agent_id,omitempty"`
}

// Worker is a subagent that is running.
type Worker struct {
	ID          string    `json:"id,omitempty"`
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
	unclaimed     []string // descriptions of subagent launches no subagent has claimed yet
	recent        []Entry
	lastEventAt   time.Time
}

// NewTracker starts idle: the session is waiting for its first prompt.
func NewTracker(now time.Time) *Tracker { return &Tracker{state: Idle, since: now, lastEventAt: now} }

// Apply changes the state for one event. Unknown kinds are ignored.
func (tracker *Tracker) Apply(event Event, now time.Time) {
	tracker.lastEventAt = now
	tracker.dropStaleWorkers(now)
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
	case EventAgentCall:
		tracker.enter(Working, now)
		tracker.unclaimed = append(tracker.unclaimed, clip(event.Detail))
		if len(tracker.unclaimed) > maxWorkers {
			tracker.unclaimed = tracker.unclaimed[1:]
		}
	case EventWorkerStart:
		tracker.enter(Working, now)
		// The subagent is taken to be the launch that came last.
		description := clip(event.Detail)
		if last := len(tracker.unclaimed) - 1; last >= 0 {
			description, tracker.unclaimed = tracker.unclaimed[last], tracker.unclaimed[:last]
		}
		tracker.workers = append(tracker.workers, Worker{ID: event.AgentID, Description: description, Since: now})
		if len(tracker.workers) > maxWorkers {
			tracker.workers = tracker.workers[1:]
		}
		tracker.note("worker: "+description, now)
	case EventWorkerEnd:
		if i := slices.IndexFunc(tracker.workers, func(worker Worker) bool { return worker.ID == event.AgentID }); i >= 0 {
			tracker.note("worker done: "+tracker.workers[i].Description, now)
			tracker.workers = slices.Delete(tracker.workers, i, i+1)
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
	tracker.unclaimed = nil
}

// dropStaleWorkers forgets workers whose stop never came.
func (tracker *Tracker) dropStaleWorkers(now time.Time) {
	tracker.workers = slices.DeleteFunc(tracker.workers, func(worker Worker) bool { return now.Sub(worker.Since) >= WorkerStaleAfter })
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
	tracker.dropStaleWorkers(now)
	record := Record{
		State: tracker.state, Since: tracker.since, Updated: now, Activity: tracker.activity,
		Workers: append([]Worker(nil), tracker.workers...), Recent: append([]Entry(nil), tracker.recent...),
		Pending: mail.Pending,
	}
	if tracker.activity != "" {
		record.ActivitySince = tracker.activitySince
	}
	if tracker.state == Working && now.Sub(tracker.lastEventAt) >= WorkingStaleAfter {
		record.State, record.Since = Idle, tracker.lastEventAt.Add(WorkingStaleAfter)
	}
	if overdue := mail.OldestUnseen.Add(UnresponsiveAfter); record.State == Idle && !mail.OldestUnseen.IsZero() && !now.Before(overdue) {
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
