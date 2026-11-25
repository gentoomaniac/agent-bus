package monitor

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
)

// Sender is the adapter session the monitor posts through, as one agent. It signs like any agent.
type Sender interface {
	AgentID() string
	// Ready fails when another process holds the identity (docs/design/identity-and-trust.md).
	Ready() error
	Send(ctx context.Context, to string, message adapter.Outgoing) (string, error)
	Publish(ctx context.Context, channelName string, message adapter.Outgoing) (string, error)
	// RequestChannel and Invite ask busd to change a channel, signed as this agent
	// (docs/design/agent-channels.md).
	RequestChannel(ctx context.Context, request channelapi.Request) (channelapi.Reply, error)
	Invite(ctx context.Context, channelName, agentID string) (reply channelapi.Reply, warning string, err error)
}

// sendTypes are the types a person may send: the ones `agent-bus send` offers.
var sendTypes = []string{"message", "request", "question", "answer", "feedback", "bug_report", "feature_request", "status", "ack"}

var (
	// ErrReadOnly is returned by Send when the monitor has no identity to send as.
	ErrReadOnly = errors.New("the monitor is read-only")
	// ErrInvalidRequest wraps the reasons a request to send is refused before anything is sent.
	ErrInvalidRequest = errors.New("invalid request")
)

// SendRequest is a message the user wrote in the interface. To names an agent (kind "agent"), or
// a group channel (kind "group"; `general` is the group named "general"), as in an envelope.
// Targets are the agents picked in the interface; without any, the @mentions in the text count.
type SendRequest struct {
	To      envelope.To `json:"to"`
	Type    string      `json:"type"`
	Targets []string    `json:"targets"`
	Title   string      `json:"title"`
	Text    string      `json:"text"`
}

// Send posts the message as the monitor's agent and returns its ID. It then arrives through the
// monitor's own feed like any other message.
func (monitor *Monitor) Send(ctx context.Context, request SendRequest) (string, error) {
	if status := monitor.Status(); !status.CanSend {
		return "", fmt.Errorf("%w: %s", ErrReadOnly, status.Note)
	}
	if !slices.Contains(sendTypes, request.Type) {
		return "", fmt.Errorf("%w: unknown message type %q, want one of %v", ErrInvalidRequest, request.Type, sendTypes)
	}
	if request.Title == "" && request.Text == "" {
		return "", fmt.Errorf("%w: the message has neither a title nor a text", ErrInvalidRequest)
	}
	message := adapter.Outgoing{Type: request.Type, Body: envelope.Body{Title: request.Title, Text: request.Text}}
	if len(request.Targets) > 0 {
		if err := (envelope.Envelope{Targets: request.Targets}).CheckTargets(); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		for _, target := range request.Targets {
			if _, err := monitor.registry.Lookup(ctx, target); target != envelope.TargetAll && err != nil {
				return "", fmt.Errorf("%w: target %v", ErrInvalidRequest, err)
			}
		}
		message.Targets = request.Targets
	}

	switch request.To.Kind {
	case envelope.ToAgent:
		if err := bus.ValidateAgentID(request.To.Name); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		if _, err := monitor.registry.Lookup(ctx, request.To.Name); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		id, err := monitor.config.Sender.Send(ctx, request.To.Name, message)
		return id, asInvalidRequest(err)
	case envelope.ToGroup:
		if request.To.Name != "general" && !monitor.hasGroup(request.To.Name) {
			return "", fmt.Errorf("%w: %q is not a channel in the catalogue", ErrInvalidRequest, request.To.Name)
		}
		id, err := monitor.config.Sender.Publish(ctx, request.To.Name, message)
		return id, asInvalidRequest(err)
	default:
		return "", fmt.Errorf("%w: unknown destination kind %q", ErrInvalidRequest, request.To.Kind)
	}
}

// asInvalidRequest turns the adapter's refusal of a message aimed at nobody into the user's mistake
// it is.
func asInvalidRequest(err error) error {
	if errors.Is(err, adapter.ErrBadTargets) {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return err
}
