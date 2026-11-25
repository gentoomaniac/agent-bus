package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
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
