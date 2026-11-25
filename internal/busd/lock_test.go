package busd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

// An approval looks at the registry only once it holds the lock: a removal that held the lock first
// has written its tombstone by then, so the approval is refused instead of recreating the account.
func TestApprovalChecksTheRegistryUnderTheLock(t *testing.T) {
	f := newFixture(t)
	reply := f.submit("late@bob", f.newKey(), "") // new ID: nothing is registered when the request comes
	if reply.Status != enroll.StatusPending {
		t.Fatalf("request: %+v", reply)
	}
	var locked, unlocked int
	f.service.config.LockMutations = func() (func(), error) {
		locked++
		removedAt := f.now
		f.registry.records["late@bob"] = recordRemoved("late@bob", removedAt) // the removal that got the lock first
		return func() { unlocked++ }, nil
	}

	if r := f.sign(reply.Code, false); !strings.Contains(r.Error, "removed") {
		t.Errorf("approving after a removal: %q", r.Error)
	}
	if len(f.registry.accounts) != 0 || len(f.registry.cursors) != 0 {
		t.Errorf("the approval changed the broker: accounts %v, cursors %v", f.registry.accounts, f.registry.cursors)
	}
	if locked != 1 || unlocked != 1 {
		t.Errorf("the lock was taken %d times and released %d times", locked, unlocked)
	}
}

func TestApprovalFailsWhenTheLockCannotBeTaken(t *testing.T) {
	f := newFixture(t)
	reply := f.submit("late@bob", f.newKey(), "")
	f.service.config.LockMutations = func() (func(), error) { return nil, errors.New("another process holds the lock") }
	if r := f.sign(reply.Code, false); !strings.Contains(r.Error, "holds the lock") || len(f.registry.accounts) != 0 {
		t.Errorf("approving without the lock: %q, accounts %v", r.Error, f.registry.accounts)
	}
	if f.service.PendingCount() != 1 {
		t.Error("the request is no longer pending, so the owner cannot try again")
	}
}

func recordRemoved(agentID string, at time.Time) bus.AgentRecord {
	return bus.AgentRecord{AgentID: agentID, RemovedAt: &at}
}
