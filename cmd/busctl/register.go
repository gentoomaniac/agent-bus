package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

type RegisterCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"The identity to create, <name>@<owner>, lowercase [a-z0-9-]."`
}

// Run creates everything an identity needs: its DM topic, SCRAM user, ACLs and registry entry on the
// broker, and its key and password on this machine. Keys are per machine and never copied, so an ID
// that is already registered anywhere is refused.
func (cmd *RegisterCmd) Run(cli *CLI) (err error) {
	if err := bus.ValidateAgentID(cmd.AgentID); err != nil {
		return err
	}
	if strings.HasPrefix(cmd.AgentID, "human@") {
		return fmt.Errorf("%s needs a password-protected key, which busctl does not support yet", cmd.AgentID)
	}
	dir := creds.AgentDir(cli.ConfigDir, cmd.AgentID)
	// Claiming the identity's directory with Mkdir makes concurrent runs for the same ID safe: only
	// the run that created it goes on, and only that run removes it again if a step fails before the
	// SCRAM user is sent, so no local key without a broker user is left behind.
	if err := os.MkdirAll(cli.ConfigDir, files.DirMode); err != nil {
		return err
	}
	admin, err := cli.adminClient()
	if err != nil {
		return err
	}
	defer admin.Close()
	ctx := context.Background()

	if err := os.Mkdir(dir, files.DirMode); errors.Is(err, fs.ErrExist) {
		// The SCRAM user is created last, so without it the earlier registration did not complete.
		registered, err := admin.HasSCRAMUser(ctx, cmd.AgentID)
		if err != nil {
			return err
		}
		if !registered {
			return fmt.Errorf("an earlier registration of %s did not complete: %s exists but the broker has no such user; "+
				"remove %s and run register again", cmd.AgentID, dir, dir)
		}
		return fmt.Errorf("%s already has a key on this machine in %s", cmd.AgentID, dir)
	} else if err != nil {
		return err
	}
	scramUserSent := false
	defer func() {
		if err == nil {
			return
		}
		if scramUserSent {
			// The broker may have applied the user although its response was lost; the key and password
			// are then the only way in, so they stay.
			fmt.Fprintf(os.Stderr, "busctl: the broker may or may not have created the SCRAM user %s. The key and password are kept in %s; "+
				"if the broker has no such user, remove that directory before registering again.\n", cmd.AgentID, dir)
			return
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			fmt.Fprintf(os.Stderr, "busctl: could not remove %s after the failed registration: %v\n", dir, rmErr)
		}
	}()

	registered, err := admin.HasSCRAMUser(ctx, cmd.AgentID)
	if err != nil {
		return err
	}
	if registered {
		return fmt.Errorf("%s is already registered on the bus; names are unique bus-wide, pick another", cmd.AgentID)
	}

	public, private := identity.Generate()
	password := creds.NewPassword()
	record, err := json.Marshal(bus.AgentRecord{
		AgentID:      cmd.AgentID,
		PublicKey:    base64.StdEncoding.EncodeToString(public),
		Fingerprint:  identity.Fingerprint(public),
		RegisteredAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}

	if err := identity.WritePrivateKey(dir, private); err != nil {
		return err
	}
	if err := creds.WritePassword(dir, password, false); err != nil {
		return err
	}

	if _, err := broker.CreateTopic(ctx, admin.Client, broker.Topic{Name: bus.DMTopic(cmd.AgentID)}); err != nil {
		return fmt.Errorf("creating the DM topic: %w", err)
	}
	if err := admin.AllowRules(ctx, cmd.AgentID, broker.AgentACLRules(cmd.AgentID)); err != nil {
		return err
	}
	// A rerun after a failure past this point finds the DM topic and ACLs in place and publishes a
	// fresh registry record; the topic is compacted, so the newer record replaces the old one.
	if err := admin.Publish(ctx, bus.RegistryTopic, cmd.AgentID, record); err != nil {
		return fmt.Errorf("writing the registry (has `busctl init` run?): %w", err)
	}
	scramUserSent = true
	if err := admin.SetSCRAMUser(ctx, cmd.AgentID, password); err != nil {
		return err
	}
	fmt.Printf("registered %s\nfingerprint %s\nkey and credentials in %s\n", cmd.AgentID, identity.Fingerprint(public), dir)
	return nil
}
