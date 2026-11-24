package monitor

import (
	"context"
	"encoding/json"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

const presenceCheckInterval = time.Second // how often agents whose heartbeat ran out are noticed

// Presence is what the monitor shows of one agent: whether it can respond and what it is doing.
type Presence struct {
	Agent         string            `json:"agent"`
	State         string            `json:"state"`
	Since         time.Time         `json:"since,omitzero"`
	Activity      string            `json:"activity,omitempty"`
	ActivitySince time.Time         `json:"activity_since,omitzero"`
	Workers       []presence.Worker `json:"workers,omitempty"`
	Recent        []presence.Entry  `json:"recent,omitempty"`
	Pending       int               `json:"pending,omitempty"`
}

// presenceOf is the view of a record at a time. An offline agent shows no activity: it is stale.
func (monitor *Monitor) presenceOf(agent string, record presence.Record, now time.Time) Presence {
	state := record.StateAt(now, monitor.offlineAfter())
	view := Presence{Agent: agent, State: state, Since: record.Since, Pending: record.Pending, Recent: record.Recent}
	if state == presence.Offline {
		if record.State != presence.Offline {
			view.Since = record.Updated // the last heartbeat is the last time it was seen
		}
		return view
	}
	view.Activity, view.ActivitySince, view.Workers = record.Activity, record.ActivitySince, record.Workers
	return view
}

func (monitor *Monitor) offlineAfter() time.Duration {
	if monitor.config.OfflineAfter > 0 {
		return monitor.config.OfflineAfter
	}
	return presence.OfflineAfter
}

// Presences lists every registered agent with its presence; one that never published is offline.
func (monitor *Monitor) Presences() []Presence {
	now := time.Now()
	monitor.presenceMu.Lock()
	defer monitor.presenceMu.Unlock()
	var views []Presence
	for _, agent := range monitor.registry.agents() {
		views = append(views, monitor.presenceOf(agent.ID, monitor.records[agent.ID], now))
	}
	return views
}

// SubscribePresence returns the presence views published from now on: every record that arrives,
// and an agent whose heartbeat ran out.
func (monitor *Monitor) SubscribePresence() (views <-chan Presence, unsubscribe func()) {
	return monitor.presenceHub.subscribe()
}

// receivePresence stores a published record and tells the subscribers.
func (monitor *Monitor) receivePresence(agent string, payload []byte) {
	var record presence.Record
	if json.Unmarshal(payload, &record) != nil {
		return
	}
	monitor.presenceMu.Lock()
	defer monitor.presenceMu.Unlock()
	if known, isKnown := monitor.records[agent]; isKnown && known.Updated.After(record.Updated) {
		return // a record read at start that a newer one already replaced
	}
	monitor.records[agent] = record
	monitor.announce(agent, record, time.Now())
}

// announce publishes the view and remembers its state, so a change of it can be told apart. The
// caller holds presenceMu.
func (monitor *Monitor) announce(agent string, record presence.Record, now time.Time) {
	view := monitor.presenceOf(agent, record, now)
	monitor.shownState[agent] = view.State
	monitor.presenceHub.publish(view)
}

// followPresence loads the records the monitor missed, watches new ones, and notices agents that
// stopped sending heartbeats. It runs until ctx ends.
func (monitor *Monitor) followPresence(ctx context.Context) {
	for ctx.Err() == nil {
		stop, err := monitor.config.Admin.WatchPresence(monitor.receivePresence)
		if err == nil {
			defer stop()
			break
		}
		monitor.report(err)
		sleep(ctx, failedReadPause)
	}
	for {
		monitor.loadMissingPresence(ctx)
		monitor.checkHeartbeats()
		select {
		case <-ctx.Done():
			return
		case <-time.After(presenceCheckInterval):
		}
	}
}

// loadMissingPresence reads the last record of each registered agent that has none yet, so the
// monitor knows about agents that published before it started.
func (monitor *Monitor) loadMissingPresence(ctx context.Context) {
	for _, agent := range monitor.registry.agents() {
		monitor.presenceMu.Lock()
		_, known := monitor.records[agent.ID]
		asked := monitor.askedPresence[agent.ID]
		monitor.presenceMu.Unlock()
		if known || asked {
			continue
		}
		payload, err := monitor.config.Admin.Presence(ctx, agent.ID)
		if err != nil {
			monitor.report(err)
			continue
		}
		monitor.presenceMu.Lock()
		monitor.askedPresence[agent.ID] = true
		monitor.presenceMu.Unlock()
		if payload != nil {
			monitor.receivePresence(agent.ID, payload)
		}
	}
}

// checkHeartbeats announces the agents that have become offline since they were last shown.
func (monitor *Monitor) checkHeartbeats() {
	now := time.Now()
	monitor.presenceMu.Lock()
	defer monitor.presenceMu.Unlock()
	for agent, record := range monitor.records {
		if monitor.shownState[agent] != presence.Offline && record.StateAt(now, monitor.offlineAfter()) == presence.Offline {
			monitor.announce(agent, record, now)
		}
	}
}
