package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// findAgent reads one agent's registry record.
func findAgent(ctx context.Context, admin bus.Admin, agentID string) (bus.AgentRecord, bool, error) {
	records, err := admin.Agents(ctx)
	if err != nil {
		return bus.AgentRecord{}, false, err
	}
	for _, record := range records {
		if record.AgentID == agentID {
			return record, true, nil
		}
	}
	return bus.AgentRecord{}, false, nil
}

// refuseRemoved fails for an ID that has a tombstone: removed IDs are never registered again, so
// certificates or pins issued for the old agent cannot end up speaking for a new one.
func refuseRemoved(ctx context.Context, admin bus.Admin, agentID string) error {
	record, found, err := findAgent(ctx, admin, agentID)
	if err != nil {
		return err
	}
	if found && record.Removed() {
		return fmt.Errorf("%s was removed and its ID is never registered again; pick another", agentID)
	}
	return nil
}

type AgentListCmd struct{}

func (cmd *AgentListCmd) Run(ctx context.Context, cli *CLI) error {
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()
	records, err := admin.Agents(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(records, func(a, b bus.AgentRecord) int { return strings.Compare(a.AgentID, b.AgentID) })
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, record := range records {
		status := "active"
		if record.Removed() {
			status = "removed"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\n", record.AgentID, status, record.Fingerprint)
	}
	return table.Flush()
}

type AgentRemoveCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"The agent to remove."`
}

func (cmd *AgentRemoveCmd) Run(ctx context.Context, cli *CLI) error {
	if err := bus.ValidateAgentID(cmd.AgentID); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := removeAgent(ctx, admin, cmd.AgentID, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Printf("removed %s: account and consumers deleted, key revoked\n"+
		"agents on other machines learn of it with `busctl directory export` and a copy (docs/busctl.md)\n", cmd.AgentID)
	return nil
}

// removeAgent runs three independent steps: the tombstone in the registry, the account (the
// reload closes its connection) and the read cursors. Each is safe to repeat, so after a partial
// failure the command is simply run again.
func removeAgent(ctx context.Context, admin bus.Admin, agentID string, now time.Time) error {
	record, found, err := findAgent(ctx, admin, agentID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s is not registered", agentID)
	}

	var failed []error
	step := func(name string, run func() error) {
		if err := run(); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", name, err))
		}
	}
	step("revoking the key", func() error { return admin.PutAgentRecord(ctx, tombstone(record, now)) })
	step("deleting the account", func() error { return admin.DeleteAccount(ctx, agentID) })
	step("deleting the consumers", func() error {
		channels := []bus.Channel{bus.DMChannel(agentID), bus.GeneralChannel()}
		groups, err := admin.GroupChannels(ctx)
		if err != nil {
			return err
		}
		for _, group := range groups {
			channels = append(channels, bus.GroupChannel(group.Name))
		}
		var errs []error
		for _, channel := range channels {
			errs = append(errs, admin.DeleteReadCursor(ctx, agentID, channel))
		}
		return errors.Join(errs...)
	})
	if len(failed) > 0 {
		return fmt.Errorf("%s is NOT completely removed; these steps failed and are left, run the command again:\n%w", agentID, errors.Join(failed...))
	}
	return nil
}

// tombstone marks the record removed and its current key revoked, keeping the key history. A
// record that is already a tombstone keeps its original times.
func tombstone(record bus.AgentRecord, now time.Time) bus.AgentRecord {
	if record.Removed() {
		return record
	}
	record.RemovedAt = &now
	record.Keys = slices.Clone(record.Keys)
	if len(record.Keys) == 0 {
		record.Keys = []bus.KeyEntry{{PublicKey: record.PublicKey, Fingerprint: record.Fingerprint, ValidFrom: record.RegisteredAt}}
	}
	current := &record.Keys[len(record.Keys)-1]
	if !current.Revoked {
		current.Revoked = true
		current.RevokedAt = &now
	}
	return record
}
