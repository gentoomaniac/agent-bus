// bus-monitor is the web interface of the agent bus (docs/design/monitor-and-api.md): it shows every channel, its
// members and the agents, and can post as one agent. It shows all traffic and speaks plain HTTP, so
// it listens on localhost or sits behind a reverse proxy; see docs/monitor.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	gocli "git.srv.gentoomaniac.net/marco/agent-messenger/internal/cli"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/logging"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"github.com/alecthomas/kong"
	"github.com/rs/zerolog/log"
)

// Set at release time by goreleaser: -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "unknown"
	binName = "bus-monitor"
	builtBy = "unknown"
	date    = "unknown"
)

const shutdownTimeout = 5 * time.Second

type CLI struct {
	logging.LoggingConfig

	Version gocli.VersionFlag `short:"V" help:"Display version."`

	Listen     string `help:"Address of the web interface. It shows all traffic and speaks plain HTTP: it must be a loopback address unless --behind-proxy is set." default:"127.0.0.1:8080" env:"BUS_WEB_LISTEN"`
	Agent      string `help:"The identity messages are sent as, <name>@<owner>, registered on this machine. Without it the interface is read-only." env:"BUS_AGENT"`
	ConfigDir  string `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker     string `help:"NATS server address (host:port); the host must be in the broker certificate." default:"localhost:4222" env:"BUS_BROKER"`
	MappedPort int    `help:"Port of the broker's certificate listener, on the host of --broker. Used instead of --broker's port for an identity directory that has client.pem and client.key." default:"4223" env:"BUS_MAPPED_PORT"`
	CACert     string `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`

	BehindProxy  bool     `help:"A reverse proxy that does the TLS sits in front: allows a non-loopback --listen address." env:"BUS_WEB_BEHIND_PROXY"`
	TrustedProxy []string `help:"Address or CIDR range of a proxy whose X-Forwarded-* headers are believed (repeatable). Default: 127.0.0.1 and ::1. Needs --behind-proxy." env:"BUS_WEB_TRUSTED_PROXY"`
	PublicURL    string   `help:"Scheme and host (and port) clients reach the proxy at, e.g. https://bus.example. Needs --behind-proxy." env:"BUS_WEB_PUBLIC_URL"`
	AllowedHost  []string `help:"A Host value to accept besides localhost, the listen address and the public URL (repeatable); without a port, any port matches." env:"BUS_WEB_ALLOWED_HOST"`

	ReconnectInitial time.Duration `help:"The first wait before reconnecting to the broker; it doubles for each failed attempt." default:"1s" env:"BUS_RECONNECT_INITIAL"`
	ReconnectMax     time.Duration `help:"The longest wait between reconnect attempts." default:"30s" env:"BUS_RECONNECT_MAX"`
}

// connection says how to reach the broker as the user whose credentials are in dir. The monitor
// outlives the broker: it starts without it and reconnects after an outage.
func (cli *CLI) connection(caCert, user, dir string) (broker.Connection, error) {
	connection, err := broker.UserConnection(dir, user, cli.Broker, cli.MappedPort, caCert)
	connection.Reconnect = &broker.Backoff{Initial: cli.ReconnectInitial, Max: cli.ReconnectMax}
	connection.Log = logging.Infof("bus-monitor: ")
	return connection, err
}

// access collects the flags that decide who may reach the page.
func (cli *CLI) access() (monitor.Access, error) {
	access := monitor.Access{Listen: cli.Listen, BehindProxy: cli.BehindProxy, AllowedHosts: cli.AllowedHost}
	var err error
	access.PasswordPath = creds.MonitorPasswordPath(cli.ConfigDir)
	if access.PasswordHash, err = creds.ReadMonitorPasswordHash(cli.ConfigDir); err != nil {
		return access, err
	}
	if access.TrustedProxies, err = monitor.ParseTrustedProxies(cli.TrustedProxy); err != nil {
		return access, err
	}
	if cli.PublicURL != "" {
		if access.PublicURL, err = url.Parse(cli.PublicURL); err != nil {
			return access, fmt.Errorf("--public-url: %w", err)
		}
	}
	return access, access.Validate()
}

// readingUsers are the broker logins the monitor reads with: its own two read-only ones, which
// `agent-busctl monitor init` copies into a config directory of its own. A config directory made
// before they existed has only the admins' logins, which the monitor then uses, with a warning:
// it can then approve enrollments, and read the CA key if it can see it.
func (cli *CLI) readingUsers() (busUser, controlUser string) {
	if _, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, broker.MonitorBusUser)); err == nil {
		return broker.MonitorBusUser, broker.MonitorControlUser
	}
	log.Warn().Msgf("bus-monitor: no read-only login in %s, so it uses the admin logins; "+
		"`agent-busctl monitor init` makes a config directory without them (docs/monitor.md)", cli.ConfigDir)
	return broker.BusAdminUser, broker.ControlAdminUser
}

func (cli *CLI) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	access, err := cli.access()
	if err != nil {
		return err
	}
	caCert := cli.CACert
	if caCert == "" {
		caCert = filepath.Join(cli.ConfigDir, "ca.pem")
	}
	busUser, controlUser := cli.readingUsers()
	busConn, err := cli.connection(caCert, busUser, creds.AdminDir(cli.ConfigDir, busUser))
	if err != nil {
		return err
	}
	controlConn, err := cli.connection(caCert, controlUser, creds.AdminDir(cli.ConfigDir, controlUser))
	if err != nil {
		return err
	}
	admin, err := broker.NewAdmin(busConn, controlConn, broker.Server{})
	if err != nil {
		return err
	}
	defer admin.Close()
	var registry bus.Admin = admin
	if controlUser == broker.MonitorControlUser {
		busdReader, err := broker.NewBusdReader(admin, controlConn)
		if err != nil {
			return err
		}
		defer busdReader.Close()
		registry = busdReader
	}
	history, err := broker.NewTransport(busConn, busUser)
	if err != nil {
		return err
	}
	defer history.Close()

	caPEM, err := os.ReadFile(caCert)
	if err != nil {
		return err
	}
	sender, readOnlyNote, closeSender, err := cli.openSender(caCert, caPEM)
	if err != nil {
		return err
	}
	defer closeSender()
	web := monitor.New(monitor.Config{
		Admin: registry, History: history, Sender: sender, ReadOnlyNote: readOnlyNote, Report: func(err error) { log.Error().Msg(err.Error()) },
		PrefsPath: creds.MonitorPrefsPath(cli.ConfigDir), DirectoryHeads: admin.WatchDirectoryHeads, CACert: caPEM,
	})
	go web.Run(ctx)

	handler, err := web.Handler(access)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cli.Listen)
	if err != nil {
		return err
	}
	// The event streams never go idle, so they end with the context instead of holding the shutdown.
	server := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()
	log.Info().Msgf("bus-monitor %s: http://%s", version, listener.Addr())
	if len(access.PasswordHash) == 0 {
		log.Warn().Msg("no password set: anyone who can reach the page sees all traffic (agent-busctl monitor password)")
	}
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openSender takes the identity of --agent, like `agent-bus --agent` does, and reads nothing with
// it. busd's channel requests go through it too: it is the bus owner (busd --bus-owner). Without an
// identity there is no sender, and the note says why the interface is read-only.
func (cli *CLI) openSender(caCert string, caPEM []byte) (sender monitor.Sender, readOnlyNote string, closeSender func(), err error) {
	closeSender = func() {}
	if cli.Agent == "" {
		return nil, "", closeSender, nil
	}
	if err := bus.ValidateAgentID(cli.Agent); err != nil {
		return nil, "", closeSender, err
	}
	if strings.HasPrefix(cli.Agent, "human@") {
		return nil, cli.Agent + " needs a password-protected key, which is not supported yet: the interface is read-only", closeSender, nil
	}
	dir := creds.AgentDir(cli.ConfigDir, cli.Agent)
	connection, err := cli.connection(caCert, bus.AgentToken(cli.Agent), dir)
	if err != nil {
		return nil, "", closeSender, err
	}
	transport, err := broker.NewTransport(connection, cli.Agent)
	if err != nil {
		return nil, "", closeSender, err
	}
	session, err := adapter.Open(adapter.Config{
		AgentID: cli.Agent, Dir: dir, Transport: transport, Directory: trust.DirectoryPath(cli.ConfigDir), ChannelLink: transport, CACert: caPEM,
	})
	if err != nil {
		transport.Close()
		return nil, "", closeSender, err
	}
	return session, "", func() { session.Close(); transport.Close() }, nil
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("bus-monitor"), kong.Vars{"version": version, "commit": commit, "binName": binName, "builtBy": builtBy, "date": date}, kong.Description("Web interface of the Agent Bus."))
	logging.Setup(&cli.LoggingConfig)
	kongCtx.FatalIfErrorf(kongCtx.Run())
}
