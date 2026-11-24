package hook

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
)

func (session *fakeSession) WaitNewMail(ctx context.Context) ([]adapter.Message, int, error) {
	select {
	case mail := <-session.mailForWaiter:
		return mail, 4, nil
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
}

// startWait runs Wait in the background, as the waiter process would.
func startWait(dir string, max time.Duration) <-chan Reply {
	replies := make(chan Reply, 1)
	go func() { replies <- Wait(context.Background(), dir, max) }()
	return replies
}

func expectNoReplyYet(t *testing.T, replies <-chan Reply) {
	t.Helper()
	select {
	case reply := <-replies:
		t.Fatalf("the waiter ended early with %+v", reply)
	case <-time.After(100 * time.Millisecond):
	}
}

func expectReply(t *testing.T, replies <-chan Reply) Reply {
	t.Helper()
	select {
	case reply := <-replies:
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not end")
		return Reply{}
	}
}

func TestWaitWakesWithTheNoticeOfTheMailThatArrives(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{titled: "the title", mailForWaiter: make(chan []adapter.Message)}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	replies := startWait(dir, time.Minute)
	expectNoReplyYet(t, replies)
	session.mailForWaiter <- session.messages()
	reply := expectReply(t, replies)
	notice := WakeOutput(reply)
	for _, want := range []string{"1 new message (4 unread)", "untrusted peer data", "`inbox`", `"the title"`} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice lacks %q: %s", want, notice)
		}
	}
	if WakeOutput(Reply{Unread: 3}) != "" {
		t.Error("a wake notice without mail")
	}
}

func TestWaitEndsEmptyWhenMaxGoesBy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	stop, err := Listen(dir, &fakeSession{mailForWaiter: make(chan []adapter.Message)})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if reply := expectReply(t, startWait(dir, 200*time.Millisecond)); len(reply.Mail) != 0 {
		t.Errorf("reply after max = %+v", reply)
	}
}

func TestANewWaiterReplacesTheOldOne(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	session := &fakeSession{mailForWaiter: make(chan []adapter.Message)}
	stop, err := Listen(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	old := startWait(dir, time.Minute)
	expectNoReplyYet(t, old)
	current := startWait(dir, time.Minute)
	if reply := expectReply(t, old); len(reply.Mail) != 0 {
		t.Errorf("the replaced waiter ended with %+v", reply)
	}
	expectNoReplyYet(t, current)
	session.mailForWaiter <- session.messages()
	if reply := expectReply(t, current); len(reply.Mail) != 1 {
		t.Errorf("the new waiter ended with %+v", reply)
	}
}

func TestWaitEndsAtOnceWithoutAnAdapter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if reply := Wait(context.Background(), dir, time.Minute); len(reply.Mail) != 0 {
		t.Errorf("wait without an adapter = %+v", reply)
	}
}
