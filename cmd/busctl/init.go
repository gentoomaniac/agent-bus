package main

import (
	"context"
	"fmt"
)

type InitCmd struct{}

// Run creates the streams and buckets of design.md §4a. Existing ones keep their data.
func (cmd *InitCmd) Run(ctx context.Context, cli *CLI) error {
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	if err := admin.Setup(ctx); err != nil {
		return err
	}
	fmt.Println("streams BUS and LEDGER and buckets registry and catalogue are in place")
	return nil
}
