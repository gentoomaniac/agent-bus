package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"golang.org/x/term"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enrollclient"
)

// waitFlag is --waitforcert[=<max>]: absent means do not wait, bare means wait as long as a
// request lives, with a value that long.
type waitFlag struct{ max time.Duration }

func (flag *waitFlag) IsBool() bool { return true } // so that the flag may stand without a value

func (flag *waitFlag) Decode(ctx *kong.DecodeContext) error {
	flag.max = enrollclient.DefaultWait
	if ctx.Scan.Peek().Type != kong.FlagValueToken {
		return nil
	}
	var text string
	if err := ctx.Scan.PopValueInto("duration", &text); err != nil {
		return err
	}
	max, err := time.ParseDuration(text)
	if err != nil || max <= 0 {
		return fmt.Errorf("--waitforcert wants a positive duration such as 10m, got %q", text)
	}
	flag.max = max
	return nil
}

type EnrollCmd struct {
	CAFingerprint  string   `name:"ca-fingerprint" help:"SHA-256 fingerprint of the CA certificate, from agent-busctl ca fingerprint on the central machine. Needed when this machine has no CA certificate yet: the CA is then fetched from the broker and kept only if it matches." env:"BUS_CA_FINGERPRINT"`
	EnrollPassword string   `name:"enroll-password" help:"Password of the shared enrollment login, from agent-busctl enroll init. Asked for on a terminal when not given." env:"BUS_ENROLL_PASSWORD"`
	Secret         string   `help:"The autosign secret, if the owner gave one: the request is then approved without them." env:"BUS_ENROLL_SECRET"`
	WaitForCert    waitFlag `name:"waitforcert" help:"Keep asking until the owner approves or rejects, for at most the given time (default 24h)." placeholder:"MAX"`
}

// Run asks the broker for a client certificate for --agent. The keys are made here and stay here;
// an existing identity keeps its signing key. It needs no login but the shared enrollment one.
func (cmd *EnrollCmd) Run(ctx context.Context, cli *CLI) error {
	if cli.Agent == "" {
		return errors.New("--agent is required")
	}
	password := cmd.EnrollPassword
	if password == "" {
		var err error
		if password, err = promptPassword("Enrollment password: "); err != nil {
			return err
		}
	}
	caCert := cli.CACert
	if caCert == "" {
		caCert = filepath.Join(cli.ConfigDir, "ca.pem")
	}
	return enrollclient.Run(ctx, enrollclient.Options{
		ConfigDir: cli.ConfigDir, AgentID: cli.Agent, Broker: cli.Broker, CACertPath: caCert, CAFingerprint: cmd.CAFingerprint,
		EnrollPassword: password, Secret: cmd.Secret, Wait: cmd.WaitForCert.max, Out: os.Stdout,
	})
}

func promptPassword(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no enrollment password: pass --enroll-password or set BUS_ENROLL_PASSWORD (`agent-busctl enroll init` shows it)")
	}
	fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(password), err
}
