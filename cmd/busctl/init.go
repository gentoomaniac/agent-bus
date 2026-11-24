package main

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
)

type InitCmd struct{}

// Run creates the system topics. It can be repeated: existing topics keep their data and get the
// configs of this version.
func (cmd *InitCmd) Run(ctx context.Context, cli *CLI) error {
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	for _, topic := range broker.SystemTopics {
		created, err := broker.CreateTopic(ctx, admin.Client, topic)
		if err != nil {
			return fmt.Errorf("creating %s: %w", topic.Name, err)
		}
		if created {
			fmt.Printf("created %s\n", topic.Name)
			continue
		}
		if err := broker.ApplyConfigs(ctx, admin.Client, topic); err != nil {
			return fmt.Errorf("updating the configs of %s: %w", topic.Name, err)
		}
		fmt.Printf("%s exists, configs applied\n", topic.Name)
	}
	return nil
}
