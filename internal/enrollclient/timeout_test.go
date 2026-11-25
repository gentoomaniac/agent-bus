package enrollclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

// silentFor is a busd that receives its first questions and does not answer them, as one that
// stopped right after: the request waits until its context ends.
type silentFor struct {
	next     Requester
	silent   int
	asked    int
	answered int
}

func (s *silentFor) Request(ctx context.Context, subject string, body []byte) ([]byte, error) {
	s.asked++
	if s.asked <= s.silent {
		_, _ = s.next.Request(ctx, subject, body)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.answered++
	return s.next.Request(ctx, subject, body)
}

func (f *fixture) runWith(requester Requester, wait, exchangeTimeout time.Duration) error {
	e := &enrolment{
		opts:      Options{ConfigDir: f.configDir, AgentID: agentID, Wait: wait, Out: f.out},
		requester: requester, caPEM: f.busd.caPEM, dir: creds.AgentDir(f.configDir, agentID),
		pollEvery: broker.Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond}, exchangeTimeout: exchangeTimeout,
	}
	return e.run(context.Background())
}

func TestAQuestionBusdNeverAnswersIsAskedAgainInsideTheWait(t *testing.T) {
	f := newFixture(t)
	f.busd.approveAfterPolls = 1                  // approved once the first status question is in
	silent := &silentFor{next: f.busd, silent: 2} // the submission and that question go unanswered

	started := time.Now()
	if err := f.runWith(silent, time.Minute, 50*time.Millisecond); err != nil {
		t.Fatalf("a lost answer ended the wait: %v", err)
	}
	if time.Since(started) > 10*time.Second || silent.answered == 0 {
		t.Errorf("took %s, %d questions answered", time.Since(started), silent.answered)
	}
	if has, _ := creds.HasClientCert(f.agentDir()); !has {
		t.Error("no certificate after busd came back")
	}
}

func TestAWaitShorterThanAMinuteIsHonoured(t *testing.T) {
	f := newFixture(t) // busd keeps saying pending
	started := time.Now()
	err := f.runWith(f.busd, 300*time.Millisecond, time.Second)
	if err == nil || !strings.Contains(err.Error(), "no decision") {
		t.Fatalf("run = %v", err)
	}
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("waited %s for a 300ms wait", elapsed)
	}
}

func TestWithoutWaitingASilentBusdEndsInOneExchangeTimeout(t *testing.T) {
	f := newFixture(t)
	started := time.Now()
	if err := f.runWith(&silentFor{next: f.busd, silent: 100}, 0, 100*time.Millisecond); err == nil {
		t.Fatal("a busd that never answers was believed")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("took %s", time.Since(started))
	}
}
