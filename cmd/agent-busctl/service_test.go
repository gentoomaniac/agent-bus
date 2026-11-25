package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

func TestAdminInitCreatesTheServiceLogins(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&AdminInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	for user, file := range map[string]string{
		broker.BusdUser: "control-auth.conf", broker.MonitorControlUser: "control-auth.conf", broker.MonitorBusUser: "auth.conf",
	} {
		if _, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, user)); err != nil {
			t.Errorf("%s has no password: %v", user, err)
		}
		body, err := os.ReadFile(filepath.Join(cli.ConfigDir, "nats", file))
		if err != nil || !strings.Contains(string(body), `{user: "`+user+`"`) {
			t.Errorf("%s lacks %s: %v", file, user, err)
		}
	}
}

func TestServiceInitAddsTheMissingLoginsOnceOnAnOlderSetup(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	adminHashes := map[string]string{broker.BusAdminUser: "$2a$10$a", broker.ControlAdminUser: "$2a$10$b"}
	if err := cli.server().WritePasswordLogins(adminHashes); err != nil {
		t.Fatal(err)
	}
	if err := (&ServiceInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	first, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, broker.BusdUser))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&ServiceInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	if again, _ := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, broker.BusdUser)); again != first {
		t.Errorf("a repeat replaced busd's password")
	}
	if err := (&ServiceInitCmd{Force: true}).Run(cli); err != nil {
		t.Fatal(err)
	}
	if forced, _ := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, broker.BusdUser)); forced == first {
		t.Errorf("--force kept the password")
	}
}
