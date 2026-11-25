package adapter

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

const (
	// channelChangeTimeout bounds a channel request that changes something: busd may take up to a
	// minute for a new channel, whose grants need a confirmed reload of the broker.
	channelChangeTimeout = 75 * time.Second
	// channelReadTimeout bounds list and members: busd answers them at once, and a caller that
	// gets no answer (busd down, a broker without the grant) falls back to the local catalogue.
	channelReadTimeout = 5 * time.Second
	// channelReadRest is how long a read that got no answer makes the next reads fail at once, so
	// that a session does not wait channelReadTimeout on every list while busd cannot be asked.
	channelReadRest = 30 * time.Second
)

// ChannelLink is the way to busd's channel requests (docs/design/agent-channels.md).
// broker.Transport implements it on the agent's own login.
type ChannelLink interface {
	RequestChannel(ctx context.Context, request []byte) ([]byte, error)
}

// RequestChannel asks busd, as this agent and signed with its key, and returns busd's verified
// answer; a refusal is the error. Every answer but a list's describes the channel. After a change
// the directory is synced, so a channel the agent joined is read from the next poll on, not only
// after busd's next head. A read (list, members) that busd does not answer makes the reads of the
// next channelReadRest fail at once with its error, until busd's next verified answer; changes are
// always tried.
func (adapter *Adapter) RequestChannel(ctx context.Context, request channelapi.Request) (channelapi.Reply, error) {
	if err := adapter.Ready(); err != nil {
		return channelapi.Reply{}, err
	}
	if adapter.config.ChannelLink == nil {
		return channelapi.Reply{}, errors.New("this session has no way to busd's channel requests")
	}
	isRead := request.Action == channelapi.List || request.Action == channelapi.Members
	timeout := channelChangeTimeout
	if isRead {
		if err := adapter.channelReadsResting(); err != nil {
			return channelapi.Reply{}, err
		}
		timeout = channelReadTimeout
	}
	request.AgentID = adapter.config.AgentID
	message, err := channelapi.Sign(request, adapter.key)
	if err != nil {
		return channelapi.Reply{}, err
	}
	askCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := adapter.config.ChannelLink.RequestChannel(askCtx, message)
	if err != nil {
		// Only busd's silence rests the reads, not a caller that gave up.
		if isRead && ctx.Err() == nil {
			adapter.restChannelReads(err)
		}
		return channelapi.Reply{}, err
	}
	reply, err := channelapi.VerifyReply(adapter.config.CACert, answer, message)
	if refusal := (channelapi.Refusal{}); err == nil || errors.As(err, &refusal) {
		adapter.restChannelReads(nil) // busd answers
	}
	if err != nil || request.Action == channelapi.List {
		return reply, err
	}
	if reply.Channel == nil {
		return channelapi.Reply{}, errors.New("busd's answer names no channel")
	}
	if !isRead {
		adapter.RequestDirectorySync()
	}
	return reply, nil
}

// channelReadsResting is the error of the last channel read that got no answer, while its rest lasts.
func (adapter *Adapter) channelReadsResting() error {
	adapter.channelReadMu.Lock()
	defer adapter.channelReadMu.Unlock()
	if adapter.channelReadErr != nil && adapter.config.Now().Before(adapter.channelReadsResume) {
		return fmt.Errorf("%w (not asking busd again until %s)", adapter.channelReadErr, clock(adapter.channelReadsResume))
	}
	return nil
}

// restChannelReads starts the rest with busd's error, or ends it with nil.
func (adapter *Adapter) restChannelReads(err error) {
	adapter.channelReadMu.Lock()
	defer adapter.channelReadMu.Unlock()
	adapter.channelReadErr, adapter.channelReadsResume = err, adapter.config.Now().Add(channelReadRest)
}

func clock(at time.Time) string { return at.UTC().Format("15:04:05Z") }

// Invite asks busd to let the agent read the channel and then tells it so in a signed DM: a
// status, which asks for no answer. The invitation stands when the DM fails; warning says why.
func (adapter *Adapter) Invite(ctx context.Context, channelName, agentID string) (reply channelapi.Reply, warning string, err error) {
	reply, err = adapter.RequestChannel(ctx, channelapi.Request{Action: channelapi.Invite, Channel: channelName, Agent: agentID})
	if err != nil {
		return reply, "", err
	}
	description := cmp.Or(reply.Channel.Description, "no description")
	notice := Outgoing{
		Type: "status", Targets: []string{agentID}, Thread: "channel:" + channelName,
		Body: envelope.Body{
			Title: "Invited to channel " + channelName,
			Text: fmt.Sprintf("%s added you to channel %s (%s). You read it from now on; leave it with the channels tool (action leave) if it is not for you.",
				adapter.config.AgentID, channelName, description),
		},
	}
	if _, err := adapter.Send(ctx, agentID, notice); err != nil {
		warning = fmt.Sprintf("%s is invited, but the notice to it was not sent: %v", agentID, err)
	}
	return reply, warning, nil
}
