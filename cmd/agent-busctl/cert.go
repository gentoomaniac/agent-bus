package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

var adminUsers = []string{broker.BusAdminUser, broker.ControlAdminUser}

func (cli *CLI) loadCA() (*tlsinit.CA, error) {
	return tlsinit.LoadCA(cli.caCertPath(), filepath.Join(cli.ConfigDir, "ca.key"))
}

// requireActiveAgent fails unless the agent is registered and not removed: a removed ID never
// gets a certificate again.
func requireActiveAgent(ctx context.Context, admin bus.Admin, agentID string) error {
	if err := bus.ValidateAgentID(agentID); err != nil {
		return err
	}
	record, found, err := findAgent(ctx, admin, agentID)
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("%s is not registered", agentID)
	case record.Removed():
		return fmt.Errorf("%s was removed and its ID is never used again", agentID)
	}
	return nil
}

type CertIssueCmd struct {
	AgentID  string        `arg:"" optional:"" name:"agent-id" help:"The registered agent to issue a certificate for."`
	Admin    bool          `help:"Issue for the two admin users instead of an agent. The web monitor logs in as them."`
	Validity time.Duration `help:"How long the certificate is valid. Renew by issuing again." default:"8760h"`
}

// Run creates a key and a certificate for the identity and writes them into its credentials
// directory (client.pem, client.key), from where the files are copied to the machine that uses
// them. The broker does not know the certificate until `cert switch` maps it.
func (cmd *CertIssueCmd) Run(ctx context.Context, cli *CLI) error {
	if cmd.Admin == (cmd.AgentID != "") {
		return errors.New("name an agent ID, or pass --admin, not both and not neither")
	}
	ca, err := cli.loadCA()
	if err != nil {
		return err
	}
	if cmd.Admin {
		for _, user := range adminUsers {
			if err := issueInto(ca, broker.AdminCertIdentity(user), creds.AdminDir(cli.ConfigDir, user), cmd.Validity); err != nil {
				return err
			}
		}
		fmt.Printf("issued certificates for %v in their directories under %s\n"+
			"next: agent-busctl --direct cert switch --admin, then restart bus-monitor\n", adminUsers, cli.ConfigDir)
		return nil
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := requireActiveAgent(ctx, admin, cmd.AgentID); err != nil {
		return err
	}
	dir := creds.AgentDir(cli.ConfigDir, cmd.AgentID)
	if err := issueInto(ca, broker.AgentCertIdentity(cmd.AgentID), dir, cmd.Validity); err != nil {
		return err
	}
	certPath, keyPath := creds.ClientCertPaths(dir)
	fmt.Printf("issued a certificate for %s: %s and %s\n"+
		"an agent on this machine uses it at once, so switch it right away: agent-busctl --direct cert switch %s\n"+
		"for another machine, copy both files into its %s directory first\n", cmd.AgentID, certPath, keyPath, cmd.AgentID, cmd.AgentID)
	return nil
}

func issueInto(ca *tlsinit.CA, identity, dir string, validity time.Duration) error {
	keyPEM, public, err := tlsinit.NewClientKey()
	if err != nil {
		return err
	}
	certPEM, err := ca.IssueClientCert(identity, public, validity)
	if err != nil {
		return err
	}
	return creds.WriteClientCert(dir, certPEM, keyPEM)
}

type CertSignCmd struct {
	AgentID  string        `arg:"" name:"agent-id" help:"The registered agent the certificate is for."`
	CSR      string        `required:"" type:"existingfile" help:"The certificate request, PEM. Only its public key is used, after its signature is checked."`
	Out      string        `type:"path" help:"Where to write the certificate. Default: standard output."`
	Validity time.Duration `help:"How long the certificate is valid." default:"8760h"`
}

// Run signs a request made on the agent's machine, so the private key never leaves it.
func (cmd *CertSignCmd) Run(ctx context.Context, cli *CLI) error {
	ca, err := cli.loadCA()
	if err != nil {
		return err
	}
	csrPEM, err := os.ReadFile(cmd.CSR)
	if err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := requireActiveAgent(ctx, admin, cmd.AgentID); err != nil {
		return err
	}
	certPEM, err := ca.SignCSR(broker.AgentCertIdentity(cmd.AgentID), csrPEM, cmd.Validity)
	if err != nil {
		return err
	}
	if cmd.Out == "" {
		_, err = os.Stdout.Write(certPEM)
		return err
	}
	return os.WriteFile(cmd.Out, certPEM, 0o644)
}

type CertSwitchCmd struct {
	AgentID  string `arg:"" optional:"" name:"agent-id" help:"The registered agent to switch."`
	Admin    bool   `help:"Switch the two admin users instead of an agent."`
	Password bool   `help:"Switch back to password login (the rollback)."`
}

// Run changes how the broker lets the identity in, and returns once the reload is confirmed. The
// agent's ID, signing key, consumers and registry record are untouched.
func (cmd *CertSwitchCmd) Run(ctx context.Context, cli *CLI) error {
	if cmd.Admin == (cmd.AgentID != "") {
		return errors.New("name an agent ID, or pass --admin, not both and not neither")
	}
	admin, err := cli.brokerAdmin()
	if err != nil {
		return err
	}
	defer admin.Close()

	if cmd.Admin {
		for _, user := range adminUsers { // bus-admin first, see Admin.SetAdminLogin
			dir := creds.AdminDir(cli.ConfigDir, user)
			if !cmd.Password && !creds.HasClientCert(dir) {
				return fmt.Errorf("%s has no certificate in %s: run `agent-busctl --direct cert issue --admin` first, or the admin tools lock themselves out", user, dir)
			}
			hash, err := cmd.passwordHash(dir)
			if err != nil {
				return err
			}
			if err := admin.SetAdminLogin(ctx, user, hash); err != nil {
				return err
			}
			if err := cmd.dropCertificate(dir); err != nil {
				return err
			}
		}
		fmt.Printf("the admin users now log in with %s; restart bus-monitor\n", cmd.mode())
		return nil
	}

	if err := requireActiveAgent(ctx, admin, cmd.AgentID); err != nil {
		return err
	}
	dir := creds.AgentDir(cli.ConfigDir, cmd.AgentID)
	hash, err := cmd.passwordHash(dir)
	if err != nil {
		return err
	}
	if err := admin.SetAgentLogin(ctx, cmd.AgentID, hash); err != nil {
		return err
	}
	if err := cmd.dropCertificate(dir); err != nil {
		return err
	}
	if cmd.Password {
		fmt.Printf("%s logs in with its password again; delete its %s and %s on every machine that has them, or agent-bus keeps using them\n",
			cmd.AgentID, "client.pem", "client.key")
	} else {
		fmt.Printf("%s now logs in with its certificate; its password no longer works\n", cmd.AgentID)
	}
	return nil
}

func (cmd *CertSwitchCmd) mode() string {
	if cmd.Password {
		return "passwords"
	}
	return "certificates"
}

// passwordHash is the hash to switch to: that of the password stored in dir (a new one is made
// if there is none), or empty for the certificate.
func (cmd *CertSwitchCmd) passwordHash(dir string) (string, error) {
	if !cmd.Password {
		return "", nil
	}
	password, err := creds.ReadPassword(dir)
	if err != nil {
		password = creds.NewPassword()
		if err := creds.WritePassword(dir, password, true); err != nil {
			return "", err
		}
	}
	return broker.HashPassword(password)
}

// dropCertificate removes the local certificate files after a switch back to the password:
// clients on this machine would use them in preference to it.
func (cmd *CertSwitchCmd) dropCertificate(dir string) error {
	if !cmd.Password {
		return nil
	}
	return creds.RemoveClientCert(dir)
}
