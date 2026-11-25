package hook

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
)

func TestOutageIsReportedOnceAfterTheGrace(t *testing.T) {
	var o outage
	down := errors.New("bus unavailable, reconnecting")
	start := time.Now()

	if o.report(down, start) != "" || o.report(down, start.Add(outageGrace/2)) != "" {
		t.Error("an outage inside the grace was reported")
	}
	if problem := o.report(down, start.Add(outageGrace)); !strings.Contains(problem, "reconnecting") {
		t.Errorf("problem = %q, want the bus error in it", problem)
	}
	if o.report(down, start.Add(2*outageGrace)) != "" {
		t.Error("the same outage was reported twice")
	}

	if o.report(nil, start.Add(3*outageGrace)) != "" {
		t.Error("a reachable bus was reported")
	}
	if o.report(down, start.Add(4*outageGrace)) != "" {
		t.Error("a new outage was reported before its grace was over")
	}
	if o.report(down, start.Add(5*outageGrace)) == "" {
		t.Error("a new outage was not reported")
	}
}

// withNoGrace lets the tests see an outage at once and a waiter notice it quickly.
func withNoGrace(t *testing.T) {
	t.Helper()
	grace, poll := outageGrace, outagePoll
	outageGrace, outagePoll = 0, 10*time.Millisecond
	t.Cleanup(func() { outageGrace, outagePoll = grace, poll })
}

func TestHooksReportAnOutageOfTheBusOnce(t *testing.T) {
	withNoGrace(t)
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if reply, _ := Ask(dir, NewMail); reply.Problem != "" {
		t.Errorf("a reachable bus was reported: %q", reply.Problem)
	}
	session.busErr = errors.New("bus unavailable, reconnecting (next attempt in 4s)")
	if reply, _ := Ask(dir, NewMail); !strings.Contains(reply.Problem, "reconnecting") || len(reply.Mail) != 1 {
		t.Errorf("reply during the outage = %+v, want the problem next to the mail", reply)
	}
	if reply, _ := Ask(dir, Summary); reply.Problem != "" {
		t.Errorf("the outage was reported twice: %q", reply.Problem)
	}
	if reply, _ := Ask(dir, PeekMail); reply.Problem != "" {
		t.Errorf("a plugin peek carries a problem: %q", reply.Problem)
	}
}

func TestWaiterEndsToReportAnOutageOfTheBus(t *testing.T) {
	withNoGrace(t)
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{busErr: errors.New("bus unavailable, reconnecting"), mailForWaiter: make(chan []adapter.Message)}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	reply := expectReply(t, startWait(dir, time.Minute))
	if len(reply.Mail) != 0 || !strings.Contains(reply.Problem, "reconnecting") {
		t.Errorf("reply = %+v, want the outage and no mail", reply)
	}
}
