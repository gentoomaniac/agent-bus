package busd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
)

type cannedRequester struct{ reply any }

func (canned cannedRequester) Request(context.Context, string, []byte) ([]byte, error) {
	return json.Marshal(canned.reply)
}

func TestTheOwnersClientPrintsNothingThatMovesATerminal(t *testing.T) {
	list := Client{cannedRequester{enroll.ListReply{Pending: []enroll.Pending{
		{RequestID: "id1", Code: "AB-CD", AgentID: "lib-x@bob", Hostname: "host\x1b[2J", Identity: "unknown: a\nb"},
	}}}}
	pending, err := list.List(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("%v, %v", pending, err)
	}
	if pending[0].Hostname != "host[2J" || pending[0].Identity != "unknown: ab" || pending[0].RequestID != "id1" {
		t.Errorf("the request as the owner reads it: %+v", pending[0])
	}

	_, err = Client{cannedRequester{enroll.ListReply{Error: "registry \x1b[2Jdown"}}}.List(context.Background())
	if err == nil || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("an error with an escape sequence: %q", err)
	}
	message, err := Client{cannedRequester{enroll.CommandReply{Message: "approved\x1b[31m x\r"}}}.Sign(context.Background(), "id1", false)
	if err != nil || strings.ContainsAny(message, "\x1b\r") {
		t.Errorf("a message with control characters: %q, %v", message, err)
	}
	_, err = Client{cannedRequester{enroll.CommandReply{Error: "no\x07pe"}}}.Reject(context.Background(), "id1", "x")
	if err == nil || strings.Contains(err.Error(), "\x07") {
		t.Errorf("an error with a bell: %q", err)
	}
}
