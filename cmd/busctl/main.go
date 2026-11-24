// busctl is the admin CLI of the agent bus (design.md §10).
package main

import (
	"path/filepath"

	"github.com/alecthomas/kong"
)

type CLI struct {
	ConfigDir string `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	CACert    string `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`

	TLS struct {
		Init TLSInitCmd `cmd:"" help:"Create the private CA and the broker certificate."`
	} `cmd:"" help:"TLS setup."`

	Admin struct {
		Init AdminInitCmd `cmd:"" help:"Create the admin credentials the broker starts with."`
	} `cmd:"" help:"Admin principal."`
}

// caCertPath is where the CA certificate lives, unless --ca-cert says otherwise.
func (cli *CLI) caCertPath() string {
	if cli.CACert != "" {
		return cli.CACert
	}
	return filepath.Join(cli.ConfigDir, "ca.pem")
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli, kong.Name("busctl"), kong.Description("Admin CLI of the agent bus."))
	ctx.FatalIfErrorf(ctx.Run(&cli))
}
