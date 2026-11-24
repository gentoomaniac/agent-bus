package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const doomed = "lib-x@bob"

// fakeAdmin implements the calls of agent removal; any other call panics on the nil interface.
type fakeAdmin struct {
	bus.Admin
	record       bus.AgentRecord
	accounts     map[string]bool
	cursors      map[string]bool
	failAccounts bool
}

func newFakeAdmin() *fakeAdmin {
	now := time.Now()
	return &fakeAdmin{
		record: bus.AgentRecord{AgentID: doomed, PublicKey: "k1", Fingerprint: "fp1", RegisteredAt: now,
			Keys: []bus.KeyEntry{{PublicKey: "k1", Fingerprint: "fp1", ValidFrom: now}}},
		accounts: map[string]bool{doomed: true},
		cursors:  map[string]bool{"DM " + doomed: true, "general": true, "channel dev": true},
	}
}

func (admin *fakeAdmin) Agents(context.Context) ([]bus.AgentRecord, error) {
	return []bus.AgentRecord{admin.record}, nil
}
func (admin *fakeAdmin) PutAgentRecord(_ context.Context, record bus.AgentRecord) error {
	admin.record = record
	return nil
}
func (admin *fakeAdmin) DeleteAccount(_ context.Context, agentID string) error {
	if admin.failAccounts {
		return errors.New("reload not confirmed")
	}
	delete(admin.accounts, agentID)
	return nil
}
func (admin *fakeAdmin) GroupChannels(context.Context) ([]bus.TopicRecord, error) {
	return []bus.TopicRecord{{Name: "dev"}}, nil
}
func (admin *fakeAdmin) ReadCursorHolders(_ context.Context, channel bus.Channel) ([]string, error) {
	if admin.cursors[channel.String()] {
		return []string{doomed}, nil
	}
	return nil, nil
}
func (admin *fakeAdmin) DeleteReadCursor(_ context.Context, _ string, channel bus.Channel) error {
	delete(admin.cursors, channel.String())
	return nil
}

func TestRemoveAgentLeavesATombstoneAndDeletesAccessAndConsumers(t *testing.T) {
	admin := newFakeAdmin()
	if err := removeAgent(context.Background(), admin, doomed, time.Now()); err != nil {
		t.Fatal(err)
	}
	key := admin.record.Keys[0]
	if !admin.record.Removed() || !key.Revoked || key.RevokedAt == nil || key.Fingerprint != "fp1" {
		t.Errorf("record is not a tombstone with its key history: %+v", admin.record)
	}
	if len(admin.accounts) != 0 || len(admin.cursors) != 0 {
		t.Errorf("left behind: accounts %v, consumers %v", admin.accounts, admin.cursors)
	}
}

func TestRemoveAgentPartialFailureNamesTheStepAndRerunFinishes(t *testing.T) {
	admin := newFakeAdmin()
	admin.failAccounts = true
	err := removeAgent(context.Background(), admin, doomed, time.Now())
	if err == nil || !strings.Contains(err.Error(), "deleting the account") || strings.Contains(err.Error(), "consumers") {
		t.Fatalf("err = %v, want only the account step to be left", err)
	}
	removedAt := admin.record.RemovedAt

	admin.failAccounts = false
	if err := removeAgent(context.Background(), admin, doomed, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if len(admin.accounts) != 0 || !admin.record.RemovedAt.Equal(*removedAt) {
		t.Errorf("rerun: accounts %v, removed_at %v (was %v)", admin.accounts, admin.record.RemovedAt, removedAt)
	}
}

func TestRemoveUnknownAgentFails(t *testing.T) {
	if err := removeAgent(context.Background(), newFakeAdmin(), "other@bob", time.Now()); err == nil {
		t.Error("an unregistered agent was removed")
	}
}

func TestRemovedIDIsNotRegisteredAgain(t *testing.T) {
	admin := newFakeAdmin()
	if err := refuseRemoved(context.Background(), admin, doomed); err != nil {
		t.Errorf("an active agent was refused: %v", err)
	}
	if err := removeAgent(context.Background(), admin, doomed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := refuseRemoved(context.Background(), admin, doomed); err == nil {
		t.Error("a removed ID was accepted for registration")
	}
}

func TestWriteCatalogueSnapshotsGroupsWithTheirReaders(t *testing.T) {
	admin := newFakeAdmin()
	dir := trust.FileDirectory(t.TempDir())
	if err := writeCatalogue(context.Background(), admin, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := dir.Catalogue(); err != nil || !reflect.DeepEqual(got, trust.Catalogue{"dev": {doomed}}) {
		t.Errorf("catalogue = %v, %v", got, err)
	}
}
