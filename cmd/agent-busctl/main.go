// agent-busctl is the admin CLI of the agent bus (docs/design/admin-cli.md).
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

// version is set at release time: -ldflags "-X main.version=...".
var version = "dev"

type CLI struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	ConfigDir  string        `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker     string        `help:"NATS server address (host:port); the host must be in the broker certificate." default:"localhost:4222" env:"BUS_BROKER"`
	MappedPort int           `help:"Port of the broker's certificate listener, on the host of --broker. Used instead of --broker's port for an admin user that has client.pem and client.key." default:"4223" env:"BUS_MAPPED_PORT"`
	CACert     string        `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`
	Timeout    time.Duration `help:"Give up after this long, for example when the broker is down." default:"30s" env:"BUS_TIMEOUT"`
	Direct     bool          `help:"Act as the admin users of the config directory, without busd. The only way until busd exists; on the broker's host, because it rewrites the server's authorization file."`
	MonitorURL string        `help:"The server's local monitoring endpoint, used to confirm reloads." default:"http://127.0.0.1:8222" env:"BUS_MONITOR_URL"`
	NATSServer string        `name:"nats-server" help:"The nats-server binary, used to test configurations. Default: bin/nats-server in the config directory." type:"path" env:"BUS_NATS_SERVER"`

	Init InitCmd `cmd:"" help:"Create the streams and buckets the bus needs. Safe to repeat."`

	Register RegisterCmd `cmd:"" help:"Register an agent identity: key, credentials, read cursors, access and registry entry."`

	Agent struct {
		List   AgentListCmd   `cmd:"" help:"List the registered agents with their status, active or removed."`
		Remove AgentRemoveCmd `cmd:"" help:"Remove an agent: delete its account and consumers and revoke its key. The ID is never reused."`
	} `cmd:"" help:"Registered agents."`

	Directory struct {
		Export DirectoryExportCmd `cmd:"" help:"Write the record of every agent, removed ones included, to a directory agents can use as their local directory."`
	} `cmd:"" help:"The agents' directory of keys."`

	Cert struct {
		Issue  CertIssueCmd  `cmd:"" help:"Create a key and a client certificate for an agent, or for the admin users, in their credentials directory."`
		Sign   CertSignCmd   `cmd:"" help:"Sign a certificate request that was made on the agent's machine."`
		Switch CertSwitchCmd `cmd:"" help:"Let an agent, or the admin users, log in with the certificate instead of the password; --password goes back."`
	} `cmd:"" help:"Client certificates for the mapped listener."`

	CA     CACmd     `cmd:"" help:"Enrollment requests and the CA, through busd (docs/agent-busctl.md)."`
	Enroll EnrollCmd `cmd:"" help:"Set up enrollment of new machines."`

	Channel struct {
		Create ChannelCreateCmd `cmd:"" help:"Create a group channel and add it to the catalogue."`
		Join   ChannelJoinCmd   `cmd:"" help:"Let a registered agent read a group channel from now on."`
	} `cmd:"" help:"Group channels."`

	Monitor struct {
		Password MonitorPasswordCmd `cmd:"" help:"Set the password of the web monitor: prompts, or reads one line from standard input."`
		Init     MonitorInitCmd     `cmd:"" help:"Create or refresh the monitor's own config directory: no CA key, no admin or busd login."`
	} `cmd:"" help:"The web monitor."`

	TLS struct {
		Init TLSInitCmd `cmd:"" help:"Create the private CA and the broker certificate."`
	} `cmd:"" help:"TLS setup."`

	Admin struct {
		Init AdminInitCmd `cmd:"" help:"Create the credentials of the two admin users, of busd and of the monitor, and the server's authorization files."`
	} `cmd:"" help:"Admin users."`

	Service struct {
		Init ServiceInitCmd `cmd:"" help:"Give busd and the monitor their own logins on a broker set up before they existed."`
	} `cmd:"" help:"The logins of busd and the monitor."`
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

// adminClient connects as the two admin users created by `agent-busctl admin init`, one connection per
// account. Without busd (milestone 3) every command that needs it goes this way, so --direct is
// required.
func (cli *CLI) adminClient() (bus.Admin, error) {
	admin, err := cli.brokerAdmin()
	if err != nil {
		return nil, err // not `return cli.brokerAdmin()`: a nil *Admin would become a non-nil bus.Admin
	}
	return admin, nil
}

// brokerAdmin is adminClient with the concrete type, for what bus.Admin does not offer.
func (cli *CLI) brokerAdmin() (*broker.Admin, error) {
	if !cli.Direct {
		return nil, errors.New("busd does not exist yet: pass --direct to act as the admin users")
	}
	connection := func(user string) (broker.Connection, error) {
		return broker.UserConnection(creds.AdminDir(cli.ConfigDir, user), user, cli.Broker, cli.MappedPort, cli.caCertPath())
	}
	busConn, err := connection(broker.BusAdminUser)
	if err != nil {
		return nil, err
	}
	controlConn, err := connection(broker.ControlAdminUser)
	if err != nil {
		return nil, err
	}
	// Held until the command ends, so that busd's approvals and other agent-busctl runs do not
	// interleave with it (Server.LockMutations).
	unlock, err := cli.server().LockMutations(cli.Timeout)
	if err != nil {
		return nil, err
	}
	admin, err := broker.NewAdmin(busConn, controlConn, cli.server())
	if err != nil {
		unlock()
		return nil, err
	}
	admin.ReleaseOnClose(unlock)
	return admin, nil
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("agent-busctl"), kong.Vars{"version": version}, kong.Description("Admin CLI of the agent bus."))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cli.Timeout)
	defer cancel()
	kongCtx.BindTo(ctx, (*context.Context)(nil))

	kongCtx.FatalIfErrorf(kongCtx.Run(&cli))
}
