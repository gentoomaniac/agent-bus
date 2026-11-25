package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
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
