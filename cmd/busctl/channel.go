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
