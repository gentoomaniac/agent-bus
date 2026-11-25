package main

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// writeCatalogue snapshots the catalogue and who reads each group into the directory, which is how
// adapters learn the channels and their members (docs/design/jetstream-binding.md). The broker is the source, so the
// snapshot is complete however often it is rewritten.
func writeCatalogue(ctx context.Context, admin bus.Admin, dir trust.FileDirectory) error {
	catalogue, err := trust.BuildCatalogue(ctx, admin)
	if err != nil {
		return err
	}
	return dir.WriteCatalogue(catalogue)
}

type ChannelCreateCmd struct {
	Name        string `arg:"" help:"Channel name, lowercase [a-z0-9-]."`
	Description string `help:"What the channel is for; listed in the catalogue."`
}

// Run adds the channel to the catalogue and gives every agent access to it. Repeating it updates
// the description.
func (cmd *ChannelCreateCmd) Run(ctx context.Context, cli *CLI) error {
	if err := bus.ValidateGroupName(cmd.Name); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	created, err := admin.CreateGroupChannel(ctx, cmd.Name, cmd.Description)
	if err != nil {
		return err
	}
	if err := writeCatalogue(ctx, admin, trust.DirectoryPath(cli.ConfigDir)); err != nil {
		return err
	}
	if created {
		fmt.Printf("created channel %s\n", cmd.Name)
	} else {
		fmt.Printf("channel %s exists, description updated\n", cmd.Name)
	}
	return nil
}

type ChannelJoinCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"The registered agent that joins."`
	Name    string `arg:"" help:"Group channel name."`
}

// Run creates the agent's read cursor on the group, starting at the current end of the log, so it
// reads what is posted from now on. The agent's grants for the cursor exist already: they come
// with `channel create`. It then rewrites the catalogue snapshot in the local directory, which the
// agent's adapter reads for its subscriptions. Repeating it leaves the cursor where it is.
func (cmd *ChannelJoinCmd) Run(ctx context.Context, cli *CLI) error {
	if err := bus.ValidateAgentID(cmd.AgentID); err != nil {
		return err
	}
	if err := bus.ValidateGroupName(cmd.Name); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	registered, err := admin.AccountExists(ctx, cmd.AgentID)
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("%s is not registered", cmd.AgentID)
	}
	last, err := admin.LastSequence(ctx)
	if err != nil {
		return err
	}
	if err := admin.CreateReadCursor(ctx, cmd.AgentID, bus.GroupChannel(cmd.Name), last); err != nil {
		return err
	}
	if err := writeCatalogue(ctx, admin, trust.DirectoryPath(cli.ConfigDir)); err != nil {
		return err
	}
	fmt.Printf("%s reads %s from now on; a running adapter of 0.2 or later picks that up within seconds, "+
		"an older one at its next start (docs/agent-busctl.md)\n", cmd.AgentID, cmd.Name)
	return nil
}
