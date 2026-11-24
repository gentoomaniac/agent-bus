package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

type RegisterCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"The identity to create, <name>@<owner>, lowercase [a-z0-9-]."`
}

// Run creates everything an identity needs: its read cursors, registry entry and account on the
// broker, and its key and password on this machine. Keys are per machine and never copied, so an ID
// that is already registered anywhere is refused.
func (cmd *RegisterCmd) Run(ctx context.Context, cli *CLI) (err error) {
	if err := bus.ValidateAgentID(cmd.AgentID); err != nil {
		return err
	}
	if strings.HasPrefix(cmd.AgentID, "human@") {
		return fmt.Errorf("%s needs a password-protected key, which busctl does not support yet", cmd.AgentID)
	}
	dir := creds.AgentDir(cli.ConfigDir, cmd.AgentID)
	// Claiming the identity's directory with Mkdir makes concurrent runs for the same ID safe: only
	// the run that created it goes on, and only that run removes it again if a step fails before the
	// account is requested, so no local key without a broker account is left behind.
	if err := os.MkdirAll(cli.ConfigDir, files.DirMode); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()

	if err := os.Mkdir(dir, files.DirMode); errors.Is(err, fs.ErrExist) {
		// The account is created last, so without it the earlier registration did not complete.
		registered, err := admin.AccountExists(ctx, cmd.AgentID)
		if err != nil {
			return err
		}
		if !registered {
			return fmt.Errorf("an earlier registration of %s did not complete: %s exists but the broker has no such account; "+
				"if no other busctl register for this ID is running, remove %s and run register again", cmd.AgentID, dir, dir)
		}
		return fmt.Errorf("%s already has a key on this machine in %s", cmd.AgentID, dir)
	} else if err != nil {
		return err
	}
	accountRequested := false
	defer func() {
		if err == nil {
			return
		}
		if accountRequested {
			// The server may have applied the account although the confirmation failed; the key and
			// password are then the only way in, so they stay.
			fmt.Fprintf(os.Stderr, "busctl: the broker may or may not have created the account %s. The key and password are kept in %s; "+
				"if the broker has no such account, remove that directory before registering again.\n", cmd.AgentID, dir)
			return
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			fmt.Fprintf(os.Stderr, "busctl: could not remove %s after the failed registration: %v\n", dir, rmErr)
		}
	}()

	registered, err := admin.AccountExists(ctx, cmd.AgentID)
	if err != nil {
		return err
	}
	if registered {
		return fmt.Errorf("%s is already registered on the bus; names are unique bus-wide, pick another", cmd.AgentID)
	}

	public, private := identity.Generate()
	password := creds.NewPassword()
	now := time.Now().UTC()
	publicKey := base64.StdEncoding.EncodeToString(public)
	fingerprint := identity.Fingerprint(public)
	record := bus.AgentRecord{
		AgentID:      cmd.AgentID,
		PublicKey:    publicKey,
		Fingerprint:  fingerprint,
		RegisteredAt: now,
		Keys:         []bus.KeyEntry{{PublicKey: publicKey, Fingerprint: fingerprint, ValidFrom: now}},
	}

	if err := identity.WritePrivateKey(dir, private); err != nil {
		return err
	}
	if err := creds.WritePassword(dir, password, false); err != nil {
		return err
	}

	// A rerun after a failure before the account finds the cursors in place and replaces the
	// registry entry.
	for _, channel := range []bus.Channel{bus.DMChannel(cmd.AgentID), bus.GeneralChannel()} {
		if err := admin.CreateReadCursor(ctx, cmd.AgentID, channel, 0); err != nil {
			return fmt.Errorf("creating the read cursor of %s: %w", channel, err)
		}
	}
	if err := admin.PutAgentRecord(ctx, record); err != nil {
		return err
	}
	if err := trust.DirectoryPath(cli.ConfigDir).Write(record); err != nil {
		return err
	}
	accountRequested = true
	if err := admin.CreateAccount(ctx, cmd.AgentID, password); err != nil {
		return err
	}
	fmt.Printf("registered %s\nfingerprint %s\nkey and credentials in %s\n", cmd.AgentID, fingerprint, dir)
	return nil
}
