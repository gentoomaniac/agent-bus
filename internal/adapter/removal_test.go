package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// remove marks the agent as removed in the registry directory, the way `busctl agent remove` and
// `busctl directory export` leave it.
func (b *testBus) remove(agentID string) {
	b.t.Helper()
	directory := trust.DirectoryPath(b.configDir)
	record, err := directory.Lookup(context.Background(), agentID)
	if err != nil {
		b.t.Fatal(err)
	}
	removedAt := b.now
	record.RemovedAt = &removedAt
	record.Keys[len(record.Keys)-1].Revoked = true
	if err := directory.Write(record); err != nil {
		b.t.Fatal(err)
	}
}

func TestRevokedKeyIsRejectedEvenWhenPinnedAfterDirectoryRefresh(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, bobTransport := b.open(bob, deliverAll)
	ctx := context.Background()
	firstID, err := a.Send(ctx, bob, text("first"))
	if err != nil {
		t.Fatal(err)
	}
	poll(t, bobAdapter)
	if err := bobAdapter.Ack(ctx, []string{firstID}); err != nil {
		t.Fatal(err)
	}
	if len(bobAdapterPins(t, b, bob)) != 1 {
		t.Fatal("alice's key was not pinned")
	}

	b.remove(alice)
	if _, err := a.Send(ctx, bob, text("second")); err != nil {
		t.Fatal(err)
	}
	if err := bobAdapter.RefreshDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	acksBefore := bobTransport.acks
	poll(t, bobAdapter)

	if inbox := inboxOf(t, bobAdapter); len(inbox) != 0 {
		t.Errorf("inbox = %+v, want the message of the revoked key rejected", inbox)
	}
	if bobTransport.acks != acksBefore+1 {
		t.Error("the rejected message was not acked")
	}
	alerts, _ := os.ReadFile(filepath.Join(b.configDir, bob, alertsFile))
	if !strings.Contains(string(alerts), "revoked key") {
		t.Errorf("alerts = %q", alerts)
	}
	reopened, err := trust.Open(filepath.Join(b.configDir, bob, knownAgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Resolve(ctx, trust.DirectoryPath(b.configDir), alice, a.Fingerprint(), b.now); err == nil {
		t.Error("the revocation was not persisted")
	}
}

func TestRemovedAgentIsRefusedAsRecipientAndNotListed(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	bobAdapter, _ := b.open(bob)
	b.remove(alice)
	ctx := context.Background()

	if _, err := bobAdapter.Send(ctx, alice, text("hello?")); err == nil || !strings.Contains(err.Error(), "agent removed") {
		t.Errorf("Send to a removed agent: %v", err)
	}
	aimedAtAlice := Outgoing{Type: "message", Body: envelope.Body{Text: "hi"}, Targets: []string{alice}}
	if _, err := bobAdapter.Publish(ctx, "general", aimedAtAlice); err == nil || !strings.Contains(err.Error(), "agent removed") {
		t.Errorf("Publish aimed at a removed agent: %v", err)
	}
	if agents, err := bobAdapter.Agents(ctx); err != nil || len(agents) != 1 || agents[0] != bob {
		t.Errorf("Agents = %v, %v", agents, err)
	}
}

func TestUnreadableRecordDoesNotStopRevocationsAndIsNotTheSendersMistake(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	a, _ := b.open(alice)
	bobAdapter, _ := b.open(bob, deliverAll)
	ctx := context.Background()
	b.remove(alice)
	badFile := filepath.Join(b.configDir, "directory", "broken_x.json")
	if err := os.WriteFile(badFile, []byte("{half a record"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := bobAdapter.RefreshDirectory(ctx)
	if err == nil || !strings.Contains(err.Error(), "broken@x") {
		t.Errorf("RefreshDirectory = %v, want the skipped file named", err)
	}
	reopened, openErr := trust.Open(filepath.Join(b.configDir, bob, knownAgentsFile))
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, err := reopened.Resolve(ctx, trust.DirectoryPath(b.configDir), alice, a.Fingerprint(), b.now); err == nil {
		t.Error("the revocation next to the bad file was not recorded")
	}
	if agents, err := bobAdapter.Agents(ctx); err != nil || len(agents) != 1 {
		t.Errorf("Agents = %v, %v", agents, err)
	}
	if _, err := bobAdapter.Send(ctx, "broken@x", text("hi")); err == nil || errors.Is(err, ErrBadTargets) {
		t.Errorf("Send with an unreadable record: %v, want a plain error", err)
	}
}
