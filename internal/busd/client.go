package busd

import (
	"context"
	"encoding/json"
	"errors"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

// Requester is a connection that sends a request to busd and returns its reply (broker.Requester).
type Requester interface {
	Request(ctx context.Context, subject string, body []byte) ([]byte, error)
}

// Client is the owner's side of busd's approval API: what `agent-busctl ca` and its review screen
// use over an admin login.
type Client struct{ Requester Requester }

func (client Client) call(ctx context.Context, subject string, command, reply any) error {
	body, err := json.Marshal(command)
	if err != nil {
		return err
	}
	answer, err := client.Requester.Request(ctx, subject, body)
	if err != nil {
		return err
	}
	return json.Unmarshal(answer, reply)
}

// List returns the pending requests, oldest first.
func (client Client) List(ctx context.Context) ([]enroll.Pending, error) {
	var reply enroll.ListReply
	if err := client.call(ctx, enroll.SubjectList, struct{}{}, &reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errors.New(enroll.Printable(reply.Error))
	}
	for i, request := range reply.Pending {
		reply.Pending[i] = request.Printable()
	}
	return reply.Pending, nil
}

// Sign approves the request named by ref (ID, code or agent ID) and says what happened.
func (client Client) Sign(ctx context.Context, ref string, replace bool) (string, error) {
	return client.command(ctx, enroll.SubjectSign, enroll.SignCommand{Ref: ref, Replace: replace})
}

// Reject refuses the request named by ref; the agent is told the reason.
func (client Client) Reject(ctx context.Context, ref, reason string) (string, error) {
	return client.command(ctx, enroll.SubjectReject, enroll.RejectCommand{Ref: ref, Reason: reason})
}

func (client Client) command(ctx context.Context, subject string, command any) (string, error) {
	var reply enroll.CommandReply
	if err := client.call(ctx, subject, command, &reply); err != nil {
		return "", err
	}
	if reply.Error != "" {
		return "", errors.New(enroll.Printable(reply.Error))
	}
	return enroll.Printable(reply.Message), nil
}
