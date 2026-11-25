package main

import (
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

// serviceUsers are the password logins of busd and the monitor, which are not admins: each gets
// only what it needs (docs/design/access-control.md).
var serviceUsers = []string{broker.BusdUser, broker.MonitorBusUser, broker.MonitorControlUser}

type AdminInitCmd struct {
	Force bool `help:"Replace existing passwords, also to finish an admin init that failed half way. The registered agents stay; a running server must be restarted to take the new ones. Admin certificates are retired (client.pem.retired, client.key.retired): the admin users log in with passwords again."`
}

// Run creates the passwords of bus-admin and control-admin, and of busd and the monitor if they
// have none, and writes the server's authorization files with their hashes (deploy/nats.sh up
// starts the server from them). NATS user names are server-wide, so each of these is its own
// user with its own password.
func (cmd *AdminInitCmd) Run(cli *CLI) error {
	hashes, err := newPasswords(cli.ConfigDir, adminUsers, cmd.Force)
	if err != nil {
		return err
	}
	serviceHashes, err := newPasswords(cli.ConfigDir, serviceUsersToInit(cli.ConfigDir, false), false)
	if err != nil {
		return err
	}
	for user, hash := range serviceHashes {
		hashes[user] = hash
	}
	server := cli.server()
	if err := server.WritePasswordLogins(hashes); err != nil {
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

// newPasswords writes a new password for each user and returns the hashes. Without overwrite, a
// user that has a password is an error.
func newPasswords(configDir string, users []string, overwrite bool) (map[string]string, error) {
	hashes := map[string]string{}
	for _, user := range users {
		password := creds.NewPassword()
		if err := creds.WritePassword(creds.AdminDir(configDir, user), password, overwrite); err != nil {
			return nil, err
		}
		hash, err := broker.HashPassword(password)
		if err != nil {
			return nil, err
		}
		hashes[user] = hash
	}
	return hashes, nil
}

// serviceUsersToInit are the service logins that have no password yet, or all of them to replace
// the passwords.
func serviceUsersToInit(configDir string, replace bool) []string {
	if replace {
		return serviceUsers
	}
	var missing []string
	for _, user := range serviceUsers {
		if _, err := creds.ReadPassword(creds.AdminDir(configDir, user)); err != nil {
			missing = append(missing, user)
		}
	}
	return missing
}

type ServiceInitCmd struct {
	Force bool `help:"Replace the passwords of busd and the monitor logins that exist already."`
}

// Run gives busd and the monitor their own logins on a broker that `admin init` set up before they
// existed (docs/podman.md, "Upgrading"). It changes the authorization files only; the broker
// reads them when it restarts.
func (cmd *ServiceInitCmd) Run(cli *CLI) error {
	hashes, err := newPasswords(cli.ConfigDir, serviceUsersToInit(cli.ConfigDir, cmd.Force), cmd.Force)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		fmt.Println("busd and the monitor have their logins already")
		return nil
	}
	if err := cli.server().WritePasswordLogins(hashes); err != nil {
		return err
	}
	fmt.Printf("logins written for %d service(s); restart the broker so it and busd use them\n", len(hashes))
	return nil
}
