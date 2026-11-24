package monitor

import (
	"testing"
	"time"
)

func TestRemovedAgentIsMarkedAndNotAMemberOfGeneral(t *testing.T) {
	alice := newTestAgent("api-client@alice")
	bob := newTestAgent("lib-x@bob")
	fake := newFakeBus(t, alice, bob)
	removedAt := time.Now()
	fake.agents[1].RemovedAt = &removedAt
	fake.agents[1].Keys[0].Revoked = true
	monitor := runMonitor(t, fake)
	server := serve(monitor)
	defer server.Close()

	var agents []Agent
	getJSON(t, server, "/api/v1/agents", &agents)
	if len(agents) != 2 || agents[0].Removed || !agents[1].Removed {
		t.Errorf("agents = %+v, want only %s removed", agents, bob.id)
	}
	var members []string
	getJSON(t, server, "/api/v1/members?channel=general", &members)
	if len(members) != 1 || members[0] != alice.id {
		t.Errorf("members = %v", members)
	}
}
