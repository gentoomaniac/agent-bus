package main

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
)

type InitCmd struct{}

// Run creates the system topics. It can be repeated: existing topics are left alone.
func (cmd *InitCmd) Run(cli *CLI) error {
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	for _, topic := range broker.SystemTopics {
		created, err := broker.CreateTopic(context.Background(), admin.Client, topic)
		if err != nil {
			return fmt.Errorf("creating %s: %w", topic.Name, err)
		}
		if created {
			fmt.Printf("created %s\n", topic.Name)
		} else {
			fmt.Printf("%s exists\n", topic.Name)
		}
	}
	return nil
}
