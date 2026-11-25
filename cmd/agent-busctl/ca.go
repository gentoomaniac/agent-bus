package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/busd"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// busdInboxPrefix is where an admin tool's answers from busd arrive; the admin login may
// subscribe anywhere.
const busdInboxPrefix = "_INBOX.agent-busctl"

// busdClient logs in as the control admin (the certificate if there is one, else the password) and
// returns busd's approval API. Unlike the --direct commands it changes nothing itself: busd, which
// holds the CA key, does the work.
func (cli *CLI) busdClient() (client busd.Client, closeClient func(), err error) {
	connection, err := broker.UserConnection(creds.AdminDir(cli.ConfigDir, broker.ControlAdminUser), broker.ControlAdminUser, cli.Broker, cli.MappedPort, cli.caCertPath())
	if err != nil {
		return busd.Client{}, nil, err
	}
	requester, err := broker.NewRequester(connection, busdInboxPrefix)
	if err != nil {
		return busd.Client{}, nil, err
	}
	return busd.Client{Requester: requester}, requester.Close, nil
}

type CACmd struct {
	List        CAListCmd        `cmd:"" help:"List the pending enrollment requests."`
	Sign        CASignCmd        `cmd:"" help:"Approve a request: busd builds its certificate and sets the agent up."`
	Reject      CARejectCmd      `cmd:"" help:"Reject a request."`
	Fingerprint CAFingerprintCmd `cmd:"" help:"Print the CA certificate's SHA-256 fingerprint, for a new machine to check its CA against."`
}

type CAListCmd struct{}

func (cmd *CAListCmd) Run(ctx context.Context, cli *CLI) error {
	client, closeClient, err := cli.busdClient()
	if err != nil {
		return err
	}
	defer closeClient()
	pending, err := client.List(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Println("no pending enrollment requests")
		return nil
	}
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "CODE\tAGENT\tHOSTNAME\tAGE\tID STATUS\tSIGNING KEY\tTLS KEY")
	for _, request := range pending {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", request.Code, request.AgentID, request.Hostname,
			time.Since(request.SubmittedAt).Round(time.Second), request.Identity, request.SigningFingerprint, request.TLSFingerprint)
	}
	return table.Flush()
}

type CASignCmd struct {
	Ref     string `arg:"" help:"The request's code, request ID or agent ID (when only one request names it)."`
	Replace bool   `help:"Confirm that an agent ID that is registered with another key is replaced: the old key is revoked."`
}

func (cmd *CASignCmd) Run(ctx context.Context, cli *CLI) error {
	client, closeClient, err := cli.busdClient()
	if err != nil {
		return err
	}
	defer closeClient()
	message, err := client.Sign(ctx, cmd.Ref, cmd.Replace)
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

type CARejectCmd struct {
	Ref    string `arg:"" help:"The request's code, request ID or agent ID (when only one request names it)."`
	Reason string `help:"Told to the agent."`
}

func (cmd *CARejectCmd) Run(ctx context.Context, cli *CLI) error {
	client, closeClient, err := cli.busdClient()
	if err != nil {
		return err
	}
	defer closeClient()
	message, err := client.Reject(ctx, cmd.Ref, cmd.Reason)
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

type CAFingerprintCmd struct{}

// Run reads the file, not the broker, so it works with busd down: the point of the fingerprint is
// that it does not come from the enrollment endpoint.
func (cmd *CAFingerprintCmd) Run(cli *CLI) error {
	certPEM, err := os.ReadFile(cli.caCertPath())
	if err != nil {
		return err
	}
	fingerprint, err := tlsinit.FingerprintOfPEM(certPEM)
	if err != nil {
		return fmt.Errorf("%s: %w", cli.caCertPath(), err)
	}
	fmt.Println(fingerprint)
	return nil
}

type EnrollCmd struct {
	Init     EnrollInitCmd     `cmd:"" help:"Create the shared enrollment login that new machines use to ask for a certificate."`
	Autosign EnrollAutosignCmd `cmd:"" help:"Turn autosigning on (a new secret) or off: a request carrying the secret is approved without you."`
}

type EnrollInitCmd struct {
	Force bool `help:"Make a new enrollment password. Machines that have not enrolled yet need the new one."`
}

// Run makes the shared login and prints its password once. It runs in the broker's container,
// where it changes the server's authorization file and reloads, like cert switch does.
func (cmd *EnrollInitCmd) Run(ctx context.Context, cli *CLI) error {
	dir := filepath.Join(cli.ConfigDir, "enroll")
	password := creds.NewPassword()
	if err := creds.WritePassword(dir, password, cmd.Force); err != nil {
		return err
	}
	hash, err := broker.HashPassword(password)
	if err != nil {
		return err
	}
	if err := cli.server().SetEnrollmentLogin(ctx, hash); err != nil {
		return err
	}
	certPEM, err := os.ReadFile(cli.caCertPath())
	if err != nil {
		return err
	}
	fingerprint, err := tlsinit.FingerprintOfPEM(certPEM)
	if err != nil {
		return err
	}
	fmt.Printf("enrollment is on. Give this password to each new machine (shown once; it only lets a machine ask, you approve):\n\n"+
		"  %s\n\nCA fingerprint, to check on the new machine:\n\n  %s\n\n"+
		"on the new machine: agent-bus enroll --broker <host>:4222 --agent <id> --ca-fingerprint <fingerprint> --waitforcert\n"+
		"then approve with `agent-busctl ca review`\n", password, fingerprint)
	return nil
}

type EnrollAutosignCmd struct {
	Off bool `help:"Turn autosigning off."`
}

// Run writes enroll/autosign, which busd reads for every request, so no restart is needed.
func (cmd *EnrollAutosignCmd) Run(cli *CLI) error {
	path := filepath.Join(cli.ConfigDir, "enroll", "autosign")
	if cmd.Off {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("autosigning is off")
		return nil
	}
	secret := creds.NewPassword()
	if err := files.Write(path, []byte(secret+"\n"), files.SecretMode, true); err != nil {
		return err
	}
	fmt.Printf("autosigning is on: a request with this secret is approved without you (never for an ID that has another key):\n\n"+
		"  %s\n\npass it as `agent-bus enroll --secret <secret>`; `agent-busctl enroll autosign --off` ends it\n", secret)
	return nil
}
