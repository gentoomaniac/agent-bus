package main

import (
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/review"
)

type CAReviewCmd struct{}

// Run shows the pending requests on a screen, updated live, where the owner approves or rejects
// them. Everything it does goes through busd, like `ca sign` and `ca reject`.
func (cmd *CAReviewCmd) Run(cli *CLI) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("ca review needs a terminal: in the container run `podman exec -it agent-bus agent-busctl ca review`, or use ca list, sign and reject")
	}
	client, closeClient, err := cli.busdClient()
	if err != nil {
		return err
	}
	defer closeClient()
	_, err = tea.NewProgram(review.New(client), tea.WithAltScreen()).Run()
	return err
}
