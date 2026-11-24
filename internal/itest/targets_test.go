//go:build integration

package itest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// carolDocs is the third agent of the targets scenario.
const carolDocs = "docs@carol"

// TestTargets runs three agents in `general` in the default targeted mode: a request aimed at one
// agent reaches only it, the reply to it reaches the sender, and @all reaches everyone.
func TestTargets(t *testing.T) {
	agentBus := filepath.Join(t.TempDir(), "agent-bus")
	if output, err := exec.Command("go", "build", "-o", agentBus, "../../cmd/agent-bus").CombinedOutput(); err != nil {
		t.Fatalf("go build agent-bus: %v\n%s", err, output)
	}
	h := startBus(t)
	h.register(t, carolDocs)
	a, b, c := h.startMCPServer(t, agentBus, alice), h.startMCPServer(t, agentBus, bob), h.startMCPServer(t, agentBus, carolDocs)

	text, isError := a.call(t, "publish", map[string]any{"channel": "general", "type": "request", "targets": []string{bob}, "body": map[string]any{"text": "please do xyz"}})
	if isError {
		t.Fatalf("publish: %s", text)
	}
	requestID := between(t, text, `"id":"`, `"`)

	text, isError = b.call(t, "wait_for_messages", map[string]any{"timeout_s": 15})
	if isError || !strings.Contains(text, "please do xyz") || !strings.Contains(text, `"targets_me":true`) {
		t.Fatalf("bob's wait_for_messages: %v %s", isError, text)
	}
	if text, isError := b.call(t, "publish", map[string]any{"channel": "general", "type": "ack", "reply_to": requestID, "body": map[string]any{"text": "on it"}}); isError {
		t.Fatalf("ack reply: %s", text)
	}

	text, isError = a.call(t, "wait_for_messages", map[string]any{"timeout_s": 15})
	if isError || !strings.Contains(text, "on it") || !strings.Contains(text, `"reply_to":"`+requestID+`"`) {
		t.Fatalf("alice should get the reply to her own request: %v %s", isError, text)
	}

	if text, isError := a.call(t, "publish", map[string]any{"channel": "general", "body": map[string]any{"text": "@all standup in five"}}); isError {
		t.Fatalf("publish @all: %s", text)
	}
	text, isError = c.call(t, "wait_for_messages", map[string]any{"timeout_s": 15})
	if isError || !strings.Contains(text, "standup in five") || !strings.Contains(text, `"targets":["@all"]`) {
		t.Fatalf("carol's wait_for_messages: %v %s", isError, text)
	}
	for _, chatter := range []string{"please do xyz", "on it"} {
		if strings.Contains(text, chatter) {
			t.Errorf("carol, who was not targeted, got %q: %s", chatter, text)
		}
	}
}
