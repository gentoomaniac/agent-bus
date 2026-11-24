package main

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

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
// with `channel create`. Repeating it leaves the cursor where it is.
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
	fmt.Printf("%s reads %s from now on\n", cmd.AgentID, cmd.Name)
	return nil
}
