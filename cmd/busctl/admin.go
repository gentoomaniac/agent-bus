package main

import (
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

type AdminInitCmd struct {
	Force bool `help:"Replace existing credentials. A broker that already has the old admin user keeps it."`
}

// Run creates the admin password the broker is bootstrapped with (deploy/redpanda.yaml)
// and busctl later connects with.
func (cmd *AdminInitCmd) Run(cli *CLI) error {
	dir := creds.AdminDir(cli.ConfigDir)
	if err := creds.WritePassword(dir, creds.NewPassword(), cmd.Force); err != nil {
		return err
	}
	fmt.Printf("Admin credentials written to %s (user %q).\n", dir, creds.AdminUser)
	return nil
}
