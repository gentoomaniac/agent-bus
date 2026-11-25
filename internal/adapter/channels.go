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

// channelRequestTimeout bounds one channel request: busd may take up to a minute for a new
// channel, whose grants need a confirmed reload of the broker.
const channelRequestTimeout = 75 * time.Second

// ChannelLink is the way to busd's channel requests (docs/design/agent-channels.md).
// broker.Transport implements it on the agent's own login.
type ChannelLink interface {
	RequestChannel(ctx context.Context, request []byte) ([]byte, error)
}

// RequestChannel asks busd, as this agent and signed with its key, and returns busd's verified
// answer; a refusal is the error. Every answer but a list's describes the channel. After a change
// the directory is synced, so a channel the agent joined is read from the next poll on, not only
// after busd's next head.
func (adapter *Adapter) RequestChannel(ctx context.Context, request channelapi.Request) (channelapi.Reply, error) {
	if err := adapter.Ready(); err != nil {
		return channelapi.Reply{}, err
	}
	if adapter.config.ChannelLink == nil {
		return channelapi.Reply{}, errors.New("this session has no way to busd's channel requests")
	}
	request.AgentID = adapter.config.AgentID
	message, err := channelapi.Sign(request, adapter.key)
	if err != nil {
		return channelapi.Reply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, channelRequestTimeout)
	defer cancel()
	answer, err := adapter.config.ChannelLink.RequestChannel(ctx, message)
	if err != nil {
		return channelapi.Reply{}, err
	}
	reply, err := channelapi.VerifyReply(adapter.config.CACert, answer, message)
	if err != nil || request.Action == channelapi.List {
		return reply, err
	}
	if reply.Channel == nil {
		return channelapi.Reply{}, errors.New("busd's answer names no channel")
	}
	adapter.RequestDirectorySync()
	return reply, nil
}

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
