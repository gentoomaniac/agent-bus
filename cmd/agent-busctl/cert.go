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
	Admin    bool          `help:"Issue for the two admin users instead of an agent, and switch them to certificate login: the admin tools use the files at once. The web monitor logs in as these users and must be restarted."`
	Switch   bool          `help:"Also switch the agent to certificate login, as cert switch does. An agent on this machine uses the files at once, so without this it is locked out until the switch. An agent that logs in with a certificate already is always switched to the new one: the old certificate stops working."`
	Validity time.Duration `help:"How long the certificate is valid (positive, and never past the CA's expiry). Renew by issuing again." default:"8760h"`
}

// Run creates a key and a certificate for the identity and writes them into its credentials
// directory (client.pem, client.key), from where the files are copied to the machine that uses
// them. The broker does not know the certificate until it is switched to certificate login, and
// it knows no other certificate of that agent afterwards.
func (cmd *CertIssueCmd) Run(ctx context.Context, cli *CLI) error {
	if cmd.Admin == (cmd.AgentID != "") {
		return errors.New("name an agent ID, or pass --admin, not both and not neither")
	}
	ca, err := cli.loadCA()
	if err != nil {
		return err
	}
	if cmd.Admin {
		if err := cli.activateStagedAdminCerts(); err != nil {
			return err
		}
	}
	// Connected before the files are written: a connection prefers the certificate once it exists.
	admin, err := cli.brokerAdmin()
	if err != nil {
		return err
	}
	defer admin.Close()

	if cmd.Admin {
		// Per admin: stage the pair, switch the server and confirm the reload, only then use the
		// pair. Until then the admin tools keep the login that works.
		for _, user := range adminUsers { // bus-admin first, see Admin.SetAdminLogin
			dir := creds.AdminDir(cli.ConfigDir, user)
			identity, certID := broker.AdminCertIdentity(user), broker.NewCertID()
			certPEM, keyPEM, err := newClientCert(ca, broker.WithCertID(identity, certID), cmd.Validity)
			if err == nil {
				err = creds.StageClientCert(dir, certPEM, keyPEM)
			}
			if err == nil {
				err = creds.ValidateStagedClientCert(dir, identity) // before the server stops accepting the password
			}
			if err == nil {
				err = admin.SetAdminLogin(ctx, user, "", certID)
			}
			if err == nil {
				err = creds.ActivateStagedClientCert(dir, identity)
			}
			if err != nil {
				return fmt.Errorf("%s was not switched to its certificate: %w\n"+
					"the admin tools keep their working login; run the command again to retry (it resumes), "+
					"or `agent-busctl --direct cert switch --admin --password` to go back to passwords", user, err)
			}
		}
		fmt.Printf("the admin users log in with certificates now (%s); restart bus-monitor\n", cli.ConfigDir)
		return nil
	}

	if err := requireActiveAgent(ctx, admin, cmd.AgentID); err != nil {
		return err
	}
	dir := creds.AgentDir(cli.ConfigDir, cmd.AgentID)
	identity, certID := broker.AgentCertIdentity(cmd.AgentID), broker.NewCertID()
	certPEM, keyPEM, err := newClientCert(ca, broker.WithCertID(identity, certID), cmd.Validity)
	if err != nil {
		return err
	}
	// Staged first: an agent on this machine keeps the login that works until the broker knows the new one.
	if err := creds.StageClientCert(dir, certPEM, keyPEM); err != nil {
		return err
	}
	_, usesCert, err := cli.server().AgentCertID(cmd.AgentID)
	if err != nil {
		return err
	}
	switched := cmd.Switch || usesCert
	if switched {
		if err := admin.SetAgentLogin(ctx, cmd.AgentID, "", certID); err != nil {
			return err
		}
	}
	if err := creds.ActivateStagedClientCert(dir, identity); err != nil {
		return err
	}
	certPath, keyPath := creds.ClientCertPaths(dir)
	fmt.Printf("issued a certificate for %s: %s and %s\n", cmd.AgentID, certPath, keyPath)
	if switched {
		fmt.Printf("%s logs in with its certificate now, and no earlier certificate of it works; for another machine, copy both files into its %s directory\n", cmd.AgentID, cmd.AgentID)
		return nil
	}
	fmt.Printf("an agent on this machine uses it at once: switch it with `agent-busctl --direct cert switch %s`\n"+
		"for another machine, copy both files into its %s directory first\n", cmd.AgentID, cmd.AgentID)
	return nil
}

func newClientCert(ca *tlsinit.CA, identity string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	keyPEM, public, err := tlsinit.NewClientKey()
	if err != nil {
		return nil, nil, err
	}
	certPEM, err = ca.IssueClientCert(identity, public, validity)
	return certPEM, keyPEM, err
}

// activateStagedAdminCerts finishes what an interrupted `cert issue --admin` left: a staged pair
// of an admin whom the server already lets in with a certificate is the only login that works, so
// it is made the login before anything connects.
func (cli *CLI) activateStagedAdminCerts() error {
	for _, user := range adminUsers {
		dir := creds.AdminDir(cli.ConfigDir, user)
		staged, err := creds.HasStagedClientCert(dir)
		if err != nil {
			return err
		}
		if !staged {
			continue
		}
		serverCertID, usesCert, err := cli.server().AdminCertID(user)
		if err != nil {
			return err
		}
		identity := broker.AdminCertIdentity(user)
		stagedURI, err := creds.StagedClientCertIdentity(dir)
		if err != nil {
			return err
		}
		// Only the certificate the server maps is a login that works.
		if usesCert && creds.MatchesIdentity(stagedURI, identity) && creds.CertID(stagedURI, identity) == serverCertID {
			if err := creds.ActivateStagedClientCert(dir, identity); err != nil {
				return err
			}
		}
	}
	return nil
}

type CertSignCmd struct {
	AgentID  string        `arg:"" name:"agent-id" help:"The registered agent the certificate is for."`
	CSR      string        `required:"" type:"existingfile" help:"The certificate request, PEM. Only its public key is used, after its signature is checked."`
	Out      string        `type:"path" help:"Where to write the certificate. Default: standard output."`
	Validity time.Duration `help:"How long the certificate is valid." default:"8760h"`
	Switch   bool          `help:"Also switch the agent to this certificate, as cert switch does: it cannot log in until the certificate is on its machine. An agent that logs in with a certificate already is always switched, and its old certificate stops working."`
}

// Run signs a request made on the agent's machine, so the private key never leaves it. The
// certificate works only once the broker knows it (cert switch --cert).
func (cmd *CertSignCmd) Run(ctx context.Context, cli *CLI) error {
	ca, err := cli.loadCA()
	if err != nil {
		return err
	}
	csrPEM, err := os.ReadFile(cmd.CSR)
	if err != nil {
		return err
	}
	admin, err := cli.brokerAdmin()
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := requireActiveAgent(ctx, admin, cmd.AgentID); err != nil {
		return err
	}
	identity, certID := broker.AgentCertIdentity(cmd.AgentID), broker.NewCertID()
	certPEM, err := ca.SignCSR(broker.WithCertID(identity, certID), csrPEM, cmd.Validity)
	if err != nil {
		return err
	}
	_, usesCert, err := cli.server().AgentCertID(cmd.AgentID)
	if err != nil {
		return err
	}
	if cmd.Switch || usesCert {
		if err := admin.SetAgentLogin(ctx, cmd.AgentID, "", certID); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stderr, "the broker does not know this certificate yet: `agent-busctl --direct cert switch %s --cert <this certificate's file>`\n", cmd.AgentID)
	}
	if cmd.Out == "" {
		_, err = os.Stdout.Write(certPEM)
		return err
	}
	return os.WriteFile(cmd.Out, certPEM, 0o644)
}

type CertSwitchCmd struct {
	AgentID  string `arg:"" optional:"" name:"agent-id" help:"The registered agent to switch."`
	Admin    bool   `help:"Switch the two admin users back to password login; needs --password. cert issue --admin switches them to certificates."`
	Password bool   `help:"Switch back to password login (the rollback)."`
	Cert     string `type:"existingfile" help:"The certificate to switch the agent to, for one made on its machine (see cert sign). Default: client.pem in the agent's directory here. Every other certificate of the agent stops working; the agent cannot log in until its files are in place."`
}

// Run changes how the broker lets the identity in, and returns once the reload is confirmed. The
// agent's ID, signing key, consumers and registry record are untouched.
func (cmd *CertSwitchCmd) Run(ctx context.Context, cli *CLI) error {
	if cmd.Admin == (cmd.AgentID != "") {
		return errors.New("name an agent ID, or pass --admin, not both and not neither")
	}
	if cmd.Admin && !cmd.Password {
		return errors.New("--admin needs --password: `cert issue --admin` is what switches the admin users to certificates, because the admin tools must use the new files at once")
	}
	if cmd.Admin { // the rollback must work after an interrupted `cert issue --admin`, too
		if err := cli.activateStagedAdminCerts(); err != nil {
			return err
		}
	}
	admin, err := cli.brokerAdmin()
	if err != nil {
		return err
	}
	defer admin.Close()

	if cmd.Admin {
		for _, user := range adminUsers { // bus-admin first, see Admin.SetAdminLogin
			dir := creds.AdminDir(cli.ConfigDir, user)
			hash, err := cmd.passwordHash(dir)
			if err != nil {
				return err
			}
			if err := admin.SetAdminLogin(ctx, user, hash, ""); err != nil {
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
	certID, err := cmd.certID(dir, broker.AgentCertIdentity(cmd.AgentID))
	if err != nil {
		return err
	}
	hash, err := cmd.passwordHash(dir)
	if err != nil {
		return err
	}
	if err := admin.SetAgentLogin(ctx, cmd.AgentID, hash, certID); err != nil {
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

// certID is the ID of the certificate to switch to, from --cert or the agent's client.pem; empty
// for a switch back to the password.
func (cmd *CertSwitchCmd) certID(dir, identity string) (string, error) {
	if cmd.Password {
		return "", nil
	}
	certPath := cmd.Cert
	if certPath == "" {
		hasCert, err := creds.HasClientCert(dir)
		if err != nil {
			return "", err
		}
		if !hasCert {
			return "", fmt.Errorf("no certificate was issued for %s here: run `cert issue %s` first, or pass --cert for one made elsewhere", cmd.AgentID, cmd.AgentID)
		}
		certPath, _ = creds.ClientCertPaths(dir)
	}
	uri, err := creds.ClientCertIdentity(certPath)
	if err != nil {
		return "", err
	}
	if !creds.MatchesIdentity(uri, identity) {
		return "", fmt.Errorf("%s is for %s, not for %s", certPath, uri, cmd.AgentID)
	}
	return creds.CertID(uri, identity), nil
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
