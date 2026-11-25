package main

import (
	"os"
	"path/filepath"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorInitCopiesOnlyWhatTheMonitorNeeds(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&AdminInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	const agent = "monitor@alice"
	writeTestFile(t, filepath.Join(cli.ConfigDir, "ca.pem"), "ca certificate")
	writeTestFile(t, filepath.Join(cli.ConfigDir, "ca.key"), "ca private key")
	writeTestFile(t, filepath.Join(cli.ConfigDir, "enroll", "autosign"), "secret")
	writeTestFile(t, filepath.Join(creds.AgentDir(cli.ConfigDir, agent), "key"), "agent key")
	writeTestFile(t, filepath.Join(creds.AgentDir(cli.ConfigDir, agent), "password"), "agent password")
	writeTestFile(t, filepath.Join(cli.ConfigDir, "directory", "monitor_alice.json"), "{}")
	writeTestFile(t, filepath.Join(cli.ConfigDir, "directory", "gone_alice.json"), "{}")
	if err := creds.WriteMonitorPassword(cli.ConfigDir, "a password for the page"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "monitor")
	cmd := &MonitorInitCmd{Dir: dir, Agent: agent}
	if err := cmd.Run(cli); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"ca.pem", "monitor-bus/password", "monitor-control/password", agent + "/key", agent + "/password",
		"monitor-web/password.bcrypt", "directory/monitor_alice.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("the monitor's config lacks %s: %v", want, err)
		}
	}
	for _, forbidden := range []string{"ca.key", "bus-admin", "control-admin", "busd", "enroll", "nats"} {
		if _, err := os.Stat(filepath.Join(dir, forbidden)); err == nil {
			t.Errorf("the monitor's config has %s", forbidden)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "monitor-bus", "password")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the monitor-bus password: %v, %v", info, err)
	}
	// What the monitor's config holds is read with the monitor's logins, and nothing else's.
	for _, user := range []string{"monitor-bus", "monitor-control"} {
		want, _ := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, user))
		if got, err := creds.ReadPassword(creds.AdminDir(dir, user)); err != nil || got != want {
			t.Errorf("%s: %q, %v", user, got, err)
		}
	}

	// A refresh drops a record that is gone and keeps the rest.
	if err := os.Remove(filepath.Join(cli.ConfigDir, "directory", "gone_alice.json")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "directory", "stale_alice.json"), "{}")
	if err := cmd.Run(cli); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"directory/gone_alice.json", "directory/stale_alice.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("a refresh left %s", gone)
		}
	}
}

func TestMonitorInitRefusesTheBrokersConfigDirectory(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&MonitorInitCmd{Dir: cli.ConfigDir}).Run(cli); err == nil {
		t.Error("the monitor was given the broker's own directory")
	}
}

func TestMonitorInitNeedsTheServiceLogins(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	writeTestFile(t, filepath.Join(cli.ConfigDir, "ca.pem"), "ca certificate")
	if err := (&MonitorInitCmd{Dir: filepath.Join(t.TempDir(), "monitor")}).Run(cli); err == nil {
		t.Error("a monitor config was made without the monitor's logins")
	}
}
