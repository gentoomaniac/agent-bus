// busctl is the admin CLI of the agent bus (design.md §10).
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"github.com/alecthomas/kong"
)

type CLI struct {
	ConfigDir  string        `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker     string        `help:"NATS server address (host:port); the host must be in the broker certificate." default:"localhost:4222" env:"BUS_BROKER"`
	CACert     string        `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`
	Timeout    time.Duration `help:"Give up after this long, for example when the broker is down." default:"30s" env:"BUS_TIMEOUT"`
	Direct     bool          `help:"Act as the admin users of the config directory, without busd. The only way until busd exists; on the broker's host, because it rewrites the server's authorization file."`
	MonitorURL string        `help:"The server's local monitoring endpoint, used to confirm reloads." default:"http://127.0.0.1:8222" env:"BUS_MONITOR_URL"`
	NATSServer string        `name:"nats-server" help:"The nats-server binary, used to test configurations. Default: bin/nats-server in the config directory." type:"path" env:"BUS_NATS_SERVER"`

	Init InitCmd `cmd:"" help:"Create the streams and buckets the bus needs. Safe to repeat."`

	Register RegisterCmd `cmd:"" help:"Register an agent identity: key, credentials, read cursors, access and registry entry."`

	Channel struct {
		Create ChannelCreateCmd `cmd:"" help:"Create a group channel and add it to the catalogue."`
		Join   ChannelJoinCmd   `cmd:"" help:"Let a registered agent read a group channel from now on."`
	} `cmd:"" help:"Group channels."`

	TLS struct {
		Init TLSInitCmd `cmd:"" help:"Create the private CA and the broker certificate."`
	} `cmd:"" help:"TLS setup."`

	Admin struct {
		Init AdminInitCmd `cmd:"" help:"Create the credentials of the two admin users and the server's authorization files."`
	} `cmd:"" help:"Admin users."`
}

// caCertPath is where the CA certificate lives, unless --ca-cert says otherwise.
func (cli *CLI) caCertPath() string {
	if cli.CACert != "" {
		return cli.CACert
	}
	return filepath.Join(cli.ConfigDir, "ca.pem")
}

func (cli *CLI) server() broker.Server {
	binary := cli.NATSServer
	if binary == "" {
		binary = filepath.Join(cli.ConfigDir, "bin", "nats-server")
	}
	return broker.NewServer(cli.ConfigDir, binary, cli.MonitorURL)
}

// adminClient connects as the two admin users created by `busctl admin init`, one connection per
// account. Without busd (milestone 3) every command that needs it goes this way, so --direct is
// required.
func (cli *CLI) adminClient() (bus.Admin, error) {
	if !cli.Direct {
		return nil, errors.New("busd does not exist yet: pass --direct to act as the admin users")
	}
	connection := func(user string) (broker.Connection, error) {
		password, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, user))
		return broker.Connection{Address: cli.Broker, CACertPath: cli.caCertPath(), User: user, Password: password}, err
	}
	busConn, err := connection(broker.BusAdminUser)
	if err != nil {
		return nil, err
	}
	controlConn, err := connection(broker.ControlAdminUser)
	if err != nil {
		return nil, err
	}
	admin, err := broker.NewAdmin(busConn, controlConn, cli.server())
	if err != nil {
		return nil, err // not `return broker.NewAdmin(...)`: a nil *Admin would become a non-nil bus.Admin
	}
	return admin, nil
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("busctl"), kong.Description("Admin CLI of the agent bus."))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()
	kongCtx.BindTo(ctx, (*context.Context)(nil))

	kongCtx.FatalIfErrorf(kongCtx.Run(&cli))
}
