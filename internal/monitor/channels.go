package monitor

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
)

// channelActions are the changes to a group channel the interface offers the bus owner.
var channelActions = []string{channelapi.Create, channelapi.Invite, channelapi.RemoveMember, channelapi.Update}

// ErrRefused wraps busd's refusal of a channel change, such as one by an agent busd does not take
// for the bus owner.
var ErrRefused = errors.New("busd refused")

// ChannelChange is a change to a group channel the user asks for in the interface. Description is
// for create and update, Agent for invite and remove_member.
type ChannelChange struct {
	Channel     string `json:"channel"`
	Description string `json:"description,omitempty"`
	Agent       string `json:"agent,omitempty"`
}

// ChangedChannel is the channel as busd describes it after the change. Warning says when an
// invitee was not told of its invitation.
type ChangedChannel struct {
	channelapi.Channel
	Warning string `json:"warning,omitempty"`
}

// ChangeChannel asks busd for the change, signed as the monitor's agent, which busd takes for the
// bus owner when it is named in --bus-owner. The monitor reads the catalogue again at once.
func (monitor *Monitor) ChangeChannel(ctx context.Context, action string, change ChannelChange) (ChangedChannel, error) {
	if status := monitor.Status(); !status.CanSend {
		return ChangedChannel{}, fmt.Errorf("%w: %s", ErrReadOnly, status.Note)
	}
	if !slices.Contains(channelActions, action) {
		return ChangedChannel{}, fmt.Errorf("%w: unknown channel action %q, want one of %v", ErrInvalidRequest, action, channelActions)
	}
	sender := monitor.config.Sender
	var reply channelapi.Reply
	var changed ChangedChannel
	var err error
	if action == channelapi.Invite {
		reply, changed.Warning, err = sender.Invite(ctx, change.Channel, change.Agent)
	} else {
		reply, err = sender.RequestChannel(ctx, channelapi.Request{Action: action, Channel: change.Channel, Description: change.Description, Agent: change.Agent})
	}
	if refusal := (channelapi.Refusal{}); errors.As(err, &refusal) {
		return ChangedChannel{}, fmt.Errorf("%w: %s", ErrRefused, refusal.Reason)
	}
	if err != nil {
		return ChangedChannel{}, err
	}
	select {
	case monitor.headSeen <- struct{}{}:
	default: // a round is due already
	}
	changed.Channel = *reply.Channel
	return changed, nil
}
