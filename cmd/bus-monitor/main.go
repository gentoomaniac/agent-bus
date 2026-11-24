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
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/monitor"
	"github.com/alecthomas/kong"
)

// version is set at release time: -ldflags "-X main.version=...".
var version = "dev"

const shutdownTimeout = 5 * time.Second

type CLI struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	Listen    string `help:"Address of the web interface. It shows all traffic and has no login: keep it on localhost." default:"127.0.0.1:8080" env:"BUS_WEB_LISTEN"`
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

	web := monitor.New(monitor.Config{Admin: admin, History: history, Report: func(err error) { log.Print(err) }})
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

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("bus-monitor"), kong.Vars{"version": version}, kong.Description("Web interface of the agent bus."))
	kongCtx.FatalIfErrorf(kongCtx.Run())
}
