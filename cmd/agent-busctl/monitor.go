package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"golang.org/x/term"
)

type MonitorPasswordCmd struct{}

// Run sets the password of the web monitor. It needs no broker: the monitor reads the hash at start.
func (cmd *MonitorPasswordCmd) Run(cli *CLI) error {
	password, err := readNewPassword(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	if err := creds.WriteMonitorPassword(cli.ConfigDir, password); err != nil {
		return err
	}
	fmt.Println("monitor password set; restart bus-monitor to use it (this also ends every login session)")
	return nil
}

// readNewPassword asks twice on a terminal, without echo. Otherwise it reads the password from the
// first line of the input, for scripts.
func readNewPassword(in io.Reader, prompts io.Writer) (string, error) {
	file, isFile := in.(*os.File)
	if !isFile || !term.IsTerminal(int(file.Fd())) {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(prompts, prompt)
		password, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(prompts)
		return string(password), err
	}
	password, err := ask("New monitor password: ")
	if err != nil {
		return "", err
	}
	again, err := ask("Again: ")
	if err != nil {
		return "", err
	}
	if password != again {
		return "", errors.New("the passwords differ")
	}
	return password, nil
}

type MonitorInitCmd struct {
	Dir   string `arg:"" type:"path" help:"The monitor's own config directory (a separate volume): it is created or refreshed."`
	Agent string `help:"The identity the monitor sends as, registered here; its key and login are copied." env:"BUS_AGENT"`
}

// Run makes the config directory of the web monitor, and refreshes it when run again. It holds
// what the monitor needs and nothing the broker's config directory keeps for the owner: the CA
// certificate (not its key), the passwords of the monitor's two read-only logins (not the admins'
// or busd's), the web password's hash, the directory snapshot and, if asked, the key and login of
// the agent it sends as. A monitor that mounts only this directory cannot sign a certificate or
// approve a request (decision 41). Run it again after agents are registered or enrolled, so the
// snapshot is current.
func (cmd *MonitorInitCmd) Run(cli *CLI) error {
	if sameDir(cmd.Dir, cli.ConfigDir) {
		return errors.New("the monitor's directory must be another one than the broker's config directory")
	}
	type transfer struct {
		from, to string
		mode     os.FileMode
	}
	copies := []transfer{{cli.caCertPath(), "ca.pem", files.PublicMode}}
	for _, user := range []string{broker.MonitorBusUser, broker.MonitorControlUser} {
		password := filepath.Join(creds.AdminDir(cli.ConfigDir, user), "password")
		if _, err := os.Stat(password); err != nil {
			return fmt.Errorf("the monitor has no login %s here: run `agent-busctl service init` and restart the broker first", user)
		}
		copies = append(copies, transfer{password, filepath.Join(user, "password"), files.SecretMode})
	}
	if cmd.Agent != "" {
		if err := bus.ValidateAgentID(cmd.Agent); err != nil {
			return err
		}
		agentDir := creds.AgentDir(cli.ConfigDir, cmd.Agent)
		if _, err := os.Stat(filepath.Join(agentDir, "key")); err != nil {
			return fmt.Errorf("%s has no key in %s: register it first", cmd.Agent, agentDir)
		}
		for name, mode := range map[string]os.FileMode{"key": files.SecretMode, "password": files.SecretMode, "client.key": files.SecretMode, "client.pem": files.PublicMode} {
			if _, err := os.Stat(filepath.Join(agentDir, name)); err == nil {
				copies = append(copies, transfer{filepath.Join(agentDir, name), filepath.Join(cmd.Agent, name), mode})
			}
		}
	}
	if webPassword := creds.MonitorPasswordPath(cli.ConfigDir); fileExists(webPassword) {
		copies = append(copies, transfer{webPassword, creds.MonitorPasswordPath(""), files.SecretMode})
	} else {
		fmt.Fprintln(os.Stderr, "no web password is set yet: `agent-busctl monitor password`, then run this again")
	}
	for _, file := range copies {
		if err := copyFile(file.from, filepath.Join(cmd.Dir, file.to), file.mode); err != nil {
			return err
		}
	}
	if err := copyDirectory(string(trust.DirectoryPath(cli.ConfigDir)), string(trust.DirectoryPath(cmd.Dir))); err != nil {
		return err
	}
	fmt.Printf("the monitor's config is in %s; mount that directory, and not %s, into the monitor\n", cmd.Dir, cli.ConfigDir)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sameDir(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func copyFile(from, to string, mode os.FileMode) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return files.Write(to, data, mode, true)
}

// copyDirectory replaces the snapshot at to with the files of from, so a removed record does not
// linger.
func copyDirectory(from, to string) error {
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	if err := os.MkdirAll(to, files.DirMode); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if err := copyFile(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name()), files.PublicMode); err != nil {
				return err
			}
		}
	}
	return nil
}
