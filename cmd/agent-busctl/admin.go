package main

import (
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

type AdminInitCmd struct {
	Force bool `help:"Replace existing passwords, also to finish an admin init that failed half way. The registered agents stay; a running server must be restarted to take the new ones. Admin certificates are retired (client.pem.retired, client.key.retired): the admin users log in with passwords again."`
}

// Run creates the passwords of bus-admin and control-admin and writes the server's authorization
// files with their hashes (deploy/nats.sh up starts the server from them). NATS user names are
// server-wide, so the admin of each account is its own user with its own password.
func (cmd *AdminInitCmd) Run(cli *CLI) error {
	hashes := map[string]string{}
	for _, user := range []string{broker.BusAdminUser, broker.ControlAdminUser} {
		password := creds.NewPassword()
		if err := creds.WritePassword(creds.AdminDir(cli.ConfigDir, user), password, cmd.Force); err != nil {
			return err
		}
		hash, err := broker.HashPassword(password)
		if err != nil {
			return err
		}
		hashes[user] = hash
	}
	server := cli.server()
	if err := server.WriteAdminAuth(hashes[broker.BusAdminUser], hashes[broker.ControlAdminUser]); err != nil {
		return err
	}
	// The server entries are passwords again, so certificates left here would make the tools and the
	// monitor try a login the server no longer maps.
	for _, user := range adminUsers {
		if err := creds.RetireClientCert(creds.AdminDir(cli.ConfigDir, user)); err != nil {
			return err
		}
	}
	fmt.Printf("Admin credentials written to %s and %s; the server's authorization files to %s.\n",
		creds.AdminDir(cli.ConfigDir, broker.BusAdminUser), creds.AdminDir(cli.ConfigDir, broker.ControlAdminUser), server.Dir)
	return nil
}
