// busd is the minimal control service of the agent bus (docs/design/busd.md, decision 41). It runs in the
// broker's container next to nats-server, holds the CA key and answers on the bus: new machines
// ask for a certificate (agent-bus enroll), the owner approves (agent-busctl ca). It has no
// HTTP interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/busd"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// version is set at release time: -ldflags "-X main.version=...".
var version = "dev"

const (
	handlerTimeout       = time.Minute // a certificate approval includes a confirmed reload
	pendingAnnounceEvery = 5 * time.Second
	// reconcileEvery is the backup round of the published directory: agent-busctl changes the
	// registry and the cursors without telling busd.
	reconcileEvery = 5 * time.Second
	// reconcileLockWait is short: a round that finds an approval running skips, the next one comes.
	reconcileLockWait = 2 * time.Second
)

type CLI struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	ConfigDir    string        `help:"Directory with the CA key, the admin credentials and the server's files." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker       string        `help:"NATS server address (host:port); busd runs next to it." default:"localhost:4222" env:"BUS_BROKER"`
	MappedPort   int           `help:"Port of the broker's certificate listener, used when an admin user has client.pem and client.key." default:"4223" env:"BUS_MAPPED_PORT"`
	CACert       string        `help:"CA certificate. Default: ca.pem in the config directory; the key is ca.key next to it." type:"path" env:"BUS_CA_CERT"`
	MonitorURL   string        `help:"The server's local monitoring endpoint, used to confirm reloads." default:"http://127.0.0.1:8222" env:"BUS_MONITOR_URL"`
	NATSServer   string        `name:"nats-server" help:"The nats-server binary, used to test configurations. Default: bin/nats-server in the config directory." type:"path" env:"BUS_NATS_SERVER"`
	CertValidity time.Duration `help:"How long an enrolled agent's certificate is valid." default:"8760h" env:"BUS_CERT_VALIDITY"`
}

// controlUser is the login busd serves CONTROL with: its own, which can do no more than busd does.
// A broker set up before it existed has none; busd then runs as the control admin, as it used to.
func (cli *CLI) controlUser() string {
	if _, err := creds.ReadPassword(creds.AdminDir(cli.ConfigDir, broker.BusdUser)); errors.Is(err, os.ErrNotExist) {
		log.Printf("busd: no login of its own in %s, so it runs with the rights of %s; "+
			"`agent-busctl service init` and a restart of the broker give it one (docs/podman.md)", cli.ConfigDir, broker.ControlAdminUser)
		return broker.ControlAdminUser
	}
	return broker.BusdUser
}

func (cli *CLI) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	caCertPath := cli.CACert
	if caCertPath == "" {
		caCertPath = filepath.Join(cli.ConfigDir, "ca.pem")
	}
	ca, err := tlsinit.LoadCA(caCertPath, filepath.Join(cli.ConfigDir, "ca.key"))
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return err
	}

	connection := func(user string) (broker.Connection, error) {
		conn, err := broker.UserConnection(creds.AdminDir(cli.ConfigDir, user), user, cli.Broker, cli.MappedPort, caCertPath)
		backoff := broker.DefaultBackoff
		conn.Reconnect, conn.Log = &backoff, log.Printf
		return conn, err
	}
	busConn, err := connection(broker.BusAdminUser)
	if err != nil {
		return err
	}
	controlConn, err := connection(cli.controlUser())
	if err != nil {
		return err
	}
	natsServer := cli.NATSServer
	if natsServer == "" {
		natsServer = filepath.Join(cli.ConfigDir, "bin", "nats-server")
	}
	server := broker.NewServer(cli.ConfigDir, natsServer, cli.MonitorURL)
	admin, err := broker.NewAdmin(busConn, controlConn, server)
	if err != nil {
		return err
	}
	defer admin.Close()

	published := busd.NewDirectory(busd.DirectoryConfig{
		Source: admin, CA: ca, ConfigDir: cli.ConfigDir,
		LockMutations: func() (func(), error) { return server.LockMutations(reconcileLockWait) },
	})
	directoryChanged := make(chan struct{}, 1)
	service, err := busd.New(busd.Config{
		Registry: admin, CA: ca, CACertPEM: caPEM, ConfigDir: cli.ConfigDir, CertValidity: cli.CertValidity,
		LockMutations: func() (func(), error) { return server.LockMutations(handlerTimeout) },
		DirectoryChanged: func() {
			select {
			case directoryChanged <- struct{}{}:
			default: // a rebuild is due already
			}
		},
	})
	if err != nil {
		return err
	}
	// The sender names the reply subject, so each endpoint answers only inside the inbox space of
	// the login that may call it: enrollees get the enrollment space, the owner's tool its own.
	type endpoint struct {
		replyPrefix string
		handle      func(context.Context, []byte) any
	}
	endpoints := map[string]endpoint{
		enroll.SubjectRequest:          {enroll.InboxPrefix, func(ctx context.Context, body []byte) any { return service.HandleRequest(ctx, body) }},
		enroll.SubjectStatus:           {enroll.InboxPrefix, func(ctx context.Context, body []byte) any { return service.HandleStatus(ctx, body) }},
		enroll.SubjectList:             {enroll.AdminInboxPrefix, func(ctx context.Context, _ []byte) any { return service.List(ctx) }},
		enroll.SubjectMonitorRegistry:  {enroll.MonitorInboxPrefix, func(ctx context.Context, _ []byte) any { return service.Agents(ctx) }},
		enroll.SubjectMonitorCatalogue: {enroll.MonitorInboxPrefix, func(ctx context.Context, _ []byte) any { return service.Groups(ctx) }},
		enroll.SubjectSign: {enroll.AdminInboxPrefix, func(ctx context.Context, body []byte) any {
			var command enroll.SignCommand
			if err := json.Unmarshal(body, &command); err != nil {
				return enroll.CommandReply{Error: "not a sign command"}
			}
			return service.Sign(ctx, command)
		}},
		enroll.SubjectReject: {enroll.AdminInboxPrefix, func(_ context.Context, body []byte) any {
			var command enroll.RejectCommand
			if err := json.Unmarshal(body, &command); err != nil {
				return enroll.CommandReply{Error: "not a reject command"}
			}
			return service.Reject(command)
		}},
	}
	for subject, endpoint := range endpoints {
		stopServing, err := admin.Serve(subject, endpoint.replyPrefix, func(body []byte) []byte {
			handlerCtx, cancel := context.WithTimeout(ctx, handlerTimeout)
			defer cancel()
			reply, _ := json.Marshal(endpoint.handle(handlerCtx, body))
			return reply
		})
		if err != nil {
			return fmt.Errorf("serving %s: %w", subject, err)
		}
		defer stopServing()
	}
	stopSync, err := admin.ServeAgents(directory.SubjectSync, published.HandleSync)
	if err != nil {
		return fmt.Errorf("serving %s.*: %w", directory.SubjectSync, err)
	}
	defer stopSync()
	log.Printf("busd %s: serving enrollment, approval and the directory on the bus", version)

	reconcile := directoryReconciler(ctx, published, admin)
	reconcile()
	// The count is all the monitor learns; announcing it often means it also hears a restart of
	// busd or of its own connection.
	announce := time.NewTicker(pendingAnnounceEvery)
	defer announce.Stop()
	rebuild := time.NewTicker(reconcileEvery)
	defer rebuild.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-announce.C:
			_ = admin.PublishPendingEnrollments(service.PendingCount())
		case <-rebuild.C:
			reconcile()
		case <-directoryChanged:
			reconcile()
		}
	}
}

// directoryReconciler rebuilds the published directory once per call and announces its head,
// changed or not: the repetition is how an agent that missed a head, or reconnected, learns the
// version. A publish that fails is simply repeated by the next call. A failure to rebuild is logged
// when it differs from the last one: a broker that is down would otherwise fill the log every few
// seconds.
func directoryReconciler(ctx context.Context, published *busd.Directory, admin *broker.Admin) func() {
	var lastErr string
	return func() {
		reconcileCtx, cancel := context.WithTimeout(ctx, handlerTimeout)
		defer cancel()
		_, err := published.Reconcile(reconcileCtx)
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != lastErr && ctx.Err() == nil {
			log.Printf("busd: rebuilding the directory: %v", err)
		}
		lastErr = message
		if head := published.Head(); head != nil {
			_ = admin.PublishDirectoryHead(head)
		}
	}
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("busd"), kong.Vars{"version": version}, kong.Description("Control service of the agent bus."))
	kongCtx.FatalIfErrorf(cli.Run())
}
