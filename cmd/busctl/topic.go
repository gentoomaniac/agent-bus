package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

type TopicCreateCmd struct {
	Name        string `arg:"" help:"Topic name, lowercase [a-z0-9-]. The Kafka topic is agents.topic.<name>."`
	Description string `help:"What the topic is for; listed in the topic catalogue."`
}

// Run creates the topic and records its description in `agents.topics`.
// Repeating it for an existing topic updates the description.
func (cmd *TopicCreateCmd) Run(ctx context.Context, cli *CLI) error {
	if err := bus.ValidateTopicName(cmd.Name); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	kafkaTopic := bus.GroupTopic(cmd.Name)
	created, err := broker.CreateTopic(ctx, admin.Client, broker.Topic{Name: kafkaTopic})
	if err != nil {
		return fmt.Errorf("creating %s: %w", kafkaTopic, err)
	}

	entry, err := json.Marshal(bus.TopicRecord{Name: cmd.Name, Description: cmd.Description, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := admin.Publish(ctx, bus.TopicsTopic, cmd.Name, entry); err != nil {
		return fmt.Errorf("writing the topic catalogue (has `busctl init` run?): %w", err)
	}

	if created {
		fmt.Printf("created %s\n", kafkaTopic)
	} else {
		fmt.Printf("%s exists, description updated\n", kafkaTopic)
	}
	return nil
}
