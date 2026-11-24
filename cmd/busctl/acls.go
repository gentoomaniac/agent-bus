package main

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

type ACLsCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"The registered identity, <name>@<owner>."`
}

// Run applies the current ACLs of an agent again, for agents registered before the rules changed.
func (cmd *ACLsCmd) Run(ctx context.Context, cli *CLI) error {
	if err := bus.ValidateAgentID(cmd.AgentID); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	registered, err := admin.HasSCRAMUser(ctx, cmd.AgentID)
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("%s is not registered on the bus", cmd.AgentID)
	}
	if err := admin.AllowRules(ctx, cmd.AgentID, broker.AgentACLRules(cmd.AgentID)); err != nil {
		return err
	}
	fmt.Printf("ACLs of %s are up to date\n", cmd.AgentID)
	return nil
}
