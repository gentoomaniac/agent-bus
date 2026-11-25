package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// silentBusd answers no channel request, as busd that is down or a broker without the grant, and
// records the time each request was given.
type silentBusd struct {
	requests  int
	timeLimit []time.Duration
}

func (busd *silentBusd) RequestChannel(ctx context.Context, _ []byte) ([]byte, error) {
	busd.requests++
	deadline, _ := ctx.Deadline()
	busd.timeLimit = append(busd.timeLimit, time.Until(deadline))
	return nil, errors.New("no responders")
}

func TestChannelReadsWaitBrieflyAndRestAfterNoAnswer(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	busd := &silentBusd{}
	session, _ := b.open(alice, func(config *Config) { config.ChannelLink = busd })
	ctx := context.Background()

	if _, err := session.RequestChannel(ctx, channelapi.Request{Action: channelapi.List}); err == nil {
		t.Fatal("a list without an answer succeeded")
	}
	if busd.timeLimit[0] > channelReadTimeout {
		t.Errorf("a list may wait %s, want at most %s", busd.timeLimit[0], channelReadTimeout)
	}
	_, err := session.RequestChannel(ctx, channelapi.Request{Action: channelapi.Members, Channel: "dev"})
	if busd.requests != 1 || err == nil || !strings.Contains(err.Error(), "no responders") || !strings.Contains(err.Error(), "not asking busd again until") {
		t.Errorf("a read in the rest: %d requests, %v", busd.requests, err)
	}
	if _, err := session.RequestChannel(ctx, channelapi.Request{Action: channelapi.Join, Channel: "dev"}); err == nil || busd.requests != 2 {
		t.Errorf("a change in the rest: %d requests, %v", busd.requests, err)
	}
	if busd.timeLimit[1] <= channelReadTimeout {
		t.Errorf("a change may wait only %s", busd.timeLimit[1])
	}

	b.now = b.now.Add(channelReadRest)
	session.RequestChannel(ctx, channelapi.Request{Action: channelapi.List})
	if busd.requests != 3 {
		t.Errorf("after the rest busd was not asked again: %d requests", busd.requests)
	}
}

// answeringBusd answers every channel request as busd would a join, signed by a CA of its own.
type answeringBusd struct {
	ca       *tlsinit.CA
	requests int
}

func (busd *answeringBusd) RequestChannel(_ context.Context, message []byte) ([]byte, error) {
	busd.requests++
	return channelapi.SignReply(busd.ca, channelapi.Reply{RequestHash: directory.RequestHash(message), Channel: &channelapi.Channel{Name: "dev"}})
}

func TestACancelledReadStartsNoRestAndAnAnswerEndsOne(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	dir := t.TempDir()
	if err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "broker.pem"), BrokerKeyPath: filepath.Join(dir, "broker.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	silent, answering := &silentBusd{}, &answeringBusd{ca: ca}
	var link ChannelLink = silent
	session, _ := b.open(alice, func(config *Config) { config.ChannelLink, config.CACert = switchingLink{&link}, caPEM })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	session.RequestChannel(cancelled, channelapi.Request{Action: channelapi.List})
	session.RequestChannel(context.Background(), channelapi.Request{Action: channelapi.List})
	if silent.requests != 2 {
		t.Errorf("a read the caller gave up on started a rest: %d requests", silent.requests)
	}

	link = answering // busd is back; a change gets through and ends the rest the last read started
	if _, err := session.RequestChannel(context.Background(), channelapi.Request{Action: channelapi.Join, Channel: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.RequestChannel(context.Background(), channelapi.Request{Action: channelapi.Members, Channel: "dev"}); err != nil {
		t.Errorf("a read after busd answered: %v", err)
	}
	if answering.requests != 2 {
		t.Errorf("busd got %d requests, want 2", answering.requests)
	}
	select {
	case <-session.syncWanted:
	default:
		t.Error("the join asked for no directory sync")
	}
	session.RequestChannel(context.Background(), channelapi.Request{Action: channelapi.Members, Channel: "dev"})
	select {
	case <-session.syncWanted:
		t.Error("members asked for a directory sync")
	default:
	}
}

// switchingLink sends to whichever link it points at, so a test can bring busd back.
type switchingLink struct{ link *ChannelLink }

func (switching switchingLink) RequestChannel(ctx context.Context, message []byte) ([]byte, error) {
	return (*switching.link).RequestChannel(ctx, message)
}
