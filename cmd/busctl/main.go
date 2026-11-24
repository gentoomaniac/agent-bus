// busctl is the admin CLI of the agent bus (design.md §10).
package main

import (
	"path/filepath"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"github.com/alecthomas/kong"
)

type CLI struct {
	ConfigDir string `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker    string `help:"Kafka broker address (host:port); the host must be in the broker certificate." default:"localhost:9092" env:"BUS_BROKER"`
	CACert    string `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`

	Init InitCmd `cmd:"" help:"Create the system topics. Safe to repeat."`

	Register RegisterCmd `cmd:"" help:"Register an agent identity: key, credentials, DM topic, ACLs and registry entry."`

	Topic struct {
		Create TopicCreateCmd `cmd:"" help:"Create a topic and add it to the topic catalogue."`
	} `cmd:"" help:"Group topics."`

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

// adminClient connects to the broker as the admin principal created by `busctl admin init`.
func (cli *CLI) adminClient() (*broker.Admin, error) {
	password, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir))
	if err != nil {
		return nil, err
	}
	return broker.NewAdmin(broker.Connection{
		Address:    cli.Broker,
		CACertPath: cli.caCertPath(),
		User:       creds.AdminUser,
		Password:   password,
	})
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli, kong.Name("busctl"), kong.Description("Admin CLI of the agent bus."))
	ctx.FatalIfErrorf(ctx.Run(&cli))
}
