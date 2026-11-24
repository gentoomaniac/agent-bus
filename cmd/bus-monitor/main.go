// bus-monitor is the web interface of the agent bus (design.md §10a): it shows every channel, its
// members and the agents, and can post as one agent. It shows all traffic and has no login, so it
// listens on localhost; see docs/monitor.md.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"github.com/alecthomas/kong"
)

// version is set at release time: -ldflags "-X main.version=...".
var version = "dev"

const shutdownTimeout = 5 * time.Second

type CLI struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	Listen    string `help:"Address of the web interface. It shows all traffic and has no login: keep it on localhost." default:"127.0.0.1:8080" env:"BUS_WEB_LISTEN"`
	Agent     string `help:"The identity messages are sent as, <name>@<owner>, registered on this machine. Without it the interface is read-only." env:"BUS_AGENT"`
	ConfigDir string `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker    string `help:"NATS server address (host:port); the host must be in the broker certificate." default:"localhost:4222" env:"BUS_BROKER"`
	CACert    string `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`
}

func (cli *CLI) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	caCert := cli.CACert
	if caCert == "" {
		caCert = filepath.Join(cli.ConfigDir, "ca.pem")
	}
	connection := func(user string) (broker.Connection, error) {
		password, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, user))
		return broker.Connection{Address: cli.Broker, CACertPath: caCert, User: user, Password: password}, err
	}
	busConn, err := connection(broker.BusAdminUser)
	if err != nil {
		return err
	}
	controlConn, err := connection(broker.ControlAdminUser)
	if err != nil {
		return err
	}
	admin, err := broker.NewAdmin(busConn, controlConn, broker.Server{})
	if err != nil {
		return err
	}
	defer admin.Close()
	history, err := broker.NewTransport(busConn, broker.BusAdminUser)
	if err != nil {
		return err
	}
	defer history.Close()

	sender, readOnlyNote, closeSender, err := cli.openSender(caCert)
	if err != nil {
		return err
	}
	defer closeSender()

	web := monitor.New(monitor.Config{Admin: admin, History: history, Sender: sender, ReadOnlyNote: readOnlyNote, Report: func(err error) { log.Print(err) }})
	go web.Run(ctx)

	listener, err := net.Listen("tcp", cli.Listen)
	if err != nil {
		return err
	}
	// The event streams never go idle, so they end with the context instead of holding the shutdown.
	server := &http.Server{Handler: web.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()
	log.Printf("bus-monitor %s: http://%s", version, listener.Addr())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openSender takes the identity of --agent, like `agent-bus --agent` does, and reads nothing with
// it. Without an identity there is no sender, and the note says why the interface is read-only.
func (cli *CLI) openSender(caCert string) (sender monitor.Sender, readOnlyNote string, closeSender func(), err error) {
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
	password, err := creds.ReadPassword(dir)
	if err != nil {
		return nil, "", closeSender, err
	}
	transport, err := broker.NewTransport(broker.Connection{Address: cli.Broker, CACertPath: caCert, User: bus.AgentToken(cli.Agent), Password: password}, cli.Agent)
	if err != nil {
		return nil, "", closeSender, err
	}
	session, err := adapter.Open(adapter.Config{AgentID: cli.Agent, Dir: dir, Transport: transport, Directory: trust.DirectoryPath(cli.ConfigDir)})
	if err != nil {
		transport.Close()
		return nil, "", closeSender, err
	}
	return session, "", func() { session.Close(); transport.Close() }, nil
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("bus-monitor"), kong.Vars{"version": version}, kong.Description("Web interface of the agent bus."))
	kongCtx.FatalIfErrorf(kongCtx.Run())
}
