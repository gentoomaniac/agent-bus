// agent-bus is the adapter's command line (docs/design/architecture.md). `mcp` is the adapter as an MCP server
// for an agent session; the other commands drive the adapter from a shell, for tests and for
// trying the bus. Each of those is its own process, so the inbox is only what one fetch brings.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/hook"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/mcpserver"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"github.com/alecthomas/kong"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at release time: -ldflags "-X main.version=...".
var version = "dev"

type CLI struct {
	Version kong.VersionFlag `help:"Print the version and exit."`

	Agent      string        `help:"The identity to act as, <name>@<owner>. Required except for pending." aliases:"identity" env:"BUS_AGENT"`
	ConfigDir  string        `help:"Directory for keys, certificates and credentials." type:"path" default:"~/.config/agent-bus" env:"BUS_CONFIG_DIR"`
	Broker     string        `help:"NATS server address (host:port); the host must be in the broker certificate." default:"localhost:4222" env:"BUS_BROKER"`
	MappedPort int           `help:"Port of the broker's certificate listener, on the host of --broker. Used instead of --broker's port when the identity directory has client.pem and client.key." default:"4223" env:"BUS_MAPPED_PORT"`
	CACert     string        `help:"CA certificate. Default: ca.pem in the config directory." type:"path" env:"BUS_CA_CERT"`
	Groups     []string      `help:"Group channels to read besides the DM channel, general and the groups that agent-busctl channel join recorded in the directory." env:"BUS_GROUPS"`
	Deliver    string        `help:"Which messages reach the agent's inbox: targeted (DMs and messages aimed at it or at @all, the default) or all (every message in its channels)." default:"targeted" enum:"targeted,all" env:"BUS_DELIVER"`
	AutoAccept bool          `name:"auto-accept-messages" negatable:"" default:"true" help:"mcp only: tell the agent it may act on a request aimed at it within its own project without asking its owner. --no-auto-accept-messages: it always asks first." env:"BUS_AUTO_ACCEPT_MESSAGES"`
	Timeout    time.Duration `help:"Give up after this long (not for mcp, which runs until its client closes it, nor pending --wait, which has --max)." default:"30s" env:"BUS_TIMEOUT"`

	ReconnectInitial time.Duration `help:"mcp only: the first wait before reconnecting to the broker; it doubles for each failed attempt." default:"1s" env:"BUS_RECONNECT_INITIAL"`
	ReconnectMax     time.Duration `help:"mcp only: the longest wait between reconnect attempts." default:"30s" env:"BUS_RECONNECT_MAX"`

	DirectoryRefresh time.Duration `help:"mcp only: how often the local directory is re-read for revoked keys (also on SIGHUP)." default:"1m" env:"BUS_DIRECTORY_REFRESH"`

	PresenceHeartbeat time.Duration `help:"mcp only: how often an unchanged presence record is published; readers call an agent offline after three times this." default:"30s" env:"BUS_PRESENCE_HEARTBEAT"`

	Enroll   EnrollCmd   `cmd:"" help:"Ask the broker for a client certificate for --agent, to be approved by the owner (docs/claude-code.md). Needs no login of the agent's own."`
	Send     SendCmd     `cmd:"" help:"Send a message to one agent."`
	Publish  PublishCmd  `cmd:"" help:"Post to general or a group channel."`
	Inbox    InboxCmd    `cmd:"" help:"Fetch new mail and print the unread messages as JSON."`
	MarkRead MarkReadCmd `cmd:"" name:"mark-read" aliases:"ack" help:"Fetch new mail and mark the given message IDs read."`
	MCP      MCPCmd      `cmd:"" name:"mcp" help:"Serve the adapter as an MCP server on stdin and stdout."`
	Pending  PendingCmd  `cmd:"" help:"For a harness hook: report this session's new mail. Asks the session's MCP server, no identity needed."`
	Event    EventCmd    `cmd:"" help:"For a harness hook: tell the session's MCP server what the harness is doing (presence). Reads the hook's JSON on stdin, prints nothing, always succeeds."`
}

type MessageFlags struct {
	Type    string   `help:"Message type." default:"message" enum:"message,request,question,answer,feedback,bug_report,feature_request,status,ack"`
	Title   string   `help:"Title of the body."`
	Text    string   `help:"Text of the body." required:""`
	Refs    []string `help:"Links the message refers to."`
	Target  []string `help:"Agent ID the message is aimed at, or @all; repeatable. Default: the @mentions in the text, the author of the message answered, or @all for a request or question; a DM targets its recipient."`
	Thread  string   `help:"Thread ID. Default: the thread of the message being answered, or a new one."`
	ReplyTo string   `help:"ID of the message this answers. Default: the message read or acked in the last five minutes."`
}

func (flags MessageFlags) outgoing() adapter.Outgoing {
	return adapter.Outgoing{
		Type: flags.Type, Body: envelope.Body{Title: flags.Title, Text: flags.Text, Refs: flags.Refs},
		Targets: flags.Target, Thread: flags.Thread, ReplyTo: flags.ReplyTo,
	}
}

type SendCmd struct {
	To string `arg:"" help:"Recipient agent ID."`
	MessageFlags
}

func (cmd *SendCmd) Run(ctx context.Context, session *adapter.Adapter) error {
	id, err := session.Send(ctx, cmd.To, cmd.outgoing())
	fmt.Println(id)
	return err
}

type PublishCmd struct {
	Channel string `arg:"" help:"general, or the name of a group channel."`
	MessageFlags
}

func (cmd *PublishCmd) Run(ctx context.Context, session *adapter.Adapter) error {
	id, err := session.Publish(ctx, cmd.Channel, cmd.outgoing())
	fmt.Println(id)
	return err
}

type InboxCmd struct {
	Limit int      `help:"At most this many messages."`
	Types []string `help:"Only these message types."`
}

func (cmd *InboxCmd) Run(ctx context.Context, session *adapter.Adapter) error {
	pollErr := session.Poll(ctx)
	messages, err := session.Inbox(adapter.InboxQuery{Limit: cmd.Limit, Types: cmd.Types})
	if err != nil {
		return err
	}
	for _, message := range messages {
		line, err := json.Marshal(struct {
			Channel      string
			FirstContact bool
			TargetsMe    bool
			Envelope     envelope.Envelope
		}{message.Channel.String(), message.FirstContact, message.TargetsMe, message.Envelope})
		if err != nil {
			return err
		}
		fmt.Println(string(line))
	}
	return pollErr
}

type MarkReadCmd struct {
	IDs []string `arg:"" help:"Message IDs to mark read."`
}

func (cmd *MarkReadCmd) Run(ctx context.Context, session *adapter.Adapter) error {
	pollErr := session.Poll(ctx)
	return errors.Join(pollErr, session.Ack(ctx, cmd.IDs))
}

type MCPCmd struct{}

// hookSession is what the hooks reach: the adapter's mail and the presence publisher's events.
type hookSession struct {
	*adapter.Adapter
	*presence.Publisher
}

func (cmd *MCPCmd) Run(ctx context.Context, cli *CLI, session *adapter.Adapter) error {
	if cli.DirectoryRefresh <= 0 {
		return errors.New("--directory-refresh must be positive")
	}
	// Only the lock holder answers hooks and publishes presence; a failure here only costs the
	// end-of-turn check and the owner's view.
	if session.Ready() == nil {
		go refreshDirectory(ctx, session, cli.DirectoryRefresh)
		publisher := presence.NewPublisher(session, cli.PresenceHeartbeat, time.Now)
		publishing := make(chan struct{})
		publishCtx, stopPublishing := context.WithCancel(ctx)
		go func() { publisher.Run(publishCtx); close(publishing) }()
		defer func() { stopPublishing(); <-publishing }()
		if stop, err := hook.Listen(hook.Dir(), hookSession{session, publisher}); err != nil {
			log.Printf("agent-bus: no hook socket: %v", err)
		} else {
			defer stop()
		}
	}
	return mcpserver.Serve(ctx, session, &mcp.StdioTransport{})
}

// refreshDirectory re-reads the local directory on a timer and on SIGHUP until ctx ends. The
// directory is copied in by hand until busd exists (docs/agent-busctl.md), so a failure is only logged.
func refreshDirectory(ctx context.Context, session *adapter.Adapter, interval time.Duration) {
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-hangups:
		}
		if err := session.RefreshDirectory(ctx); err != nil {
			log.Printf("agent-bus: refreshing the directory: %v", err)
		}
	}
}

type PendingCmd struct {
	Format string        `help:"hook: the Stop hook's decision. session-start: the SessionStart hook's context. plugin: {reason, ids} for a plugin that sends the prompt itself; nothing is marked told until --mark." default:"hook" enum:"hook,session-start,plugin"`
	Mark   []string      `help:"Mark these message IDs told, after a plugin delivered the notice, and print nothing."`
	Wait   bool          `help:"Instead of reporting at once, block until this session has new mail for it (not chatter), print the notice on stderr and exit 2: the Claude Code asyncRewake hook, which wakes an idle session. Exits 0 silently after --max, or when the session's MCP server goes away or a newer waiter replaces this one."`
	Max    time.Duration `help:"With --wait: the longest to wait. Stay below the hook's timeout." default:"23h"`
}

// wakeExitCode is the exit code with which an asyncRewake hook wakes Claude.
const wakeExitCode = 2

// Run prints nothing and succeeds unless there is mail to report: a hook must never fail its
// session. It does not read the hook's stdin (so stop_hook_active is not consulted): a message is
// reported once, so a Stop hook that fires again after its own block finds nothing new.
func (cmd *PendingCmd) Run(ctx context.Context) error {
	if len(cmd.Mark) > 0 {
		// Only the OpenCode plugin marks; it must learn when the adapter did not take the mark, or
		// it would forget the IDs and prompt for the same mail again.
		if !hook.ConfirmTold(hook.Dir(), cmd.Mark) {
			return errors.New("the session's adapter did not confirm the mark")
		}
		return nil
	}
	if cmd.Wait {
		if notice := hook.WakeOutput(hook.Wait(ctx, hook.Dir(), cmd.Max)); notice != "" {
			fmt.Fprintln(os.Stderr, notice)
			os.Exit(wakeExitCode)
		}
		return nil
	}
	op, format := hook.NewMail, hook.StopOutput
	switch cmd.Format {
	case "session-start":
		op, format = hook.Summary, hook.SessionStartOutput
	case "plugin":
		op, format = hook.PeekMail, hook.PluginOutput
	}
	if reply, ok := hook.Ask(hook.Dir(), op); ok {
		if output := format(reply); output != "" {
			fmt.Println(output)
		}
	}
	return nil
}

type EventCmd struct {
	Kind string `help:"The hook this runs for, named like the Claude Code hook." required:"" enum:"user-prompt,stop,pre-tool,post-tool,subagent-start,subagent-stop,stop-failure,notification,session-start,session-end"`
}

const (
	stdinTimeout  = time.Second // the command must never hold its session up
	maxStdinBytes = 1 << 20
)

// Run prints nothing and succeeds whatever happens: a hook must never fail or slow its session,
// and presence is only for the owner's monitor.
func (cmd *EventCmd) Run() error {
	input := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(os.Stdin, maxStdinBytes))
		input <- data
	}()
	var data []byte
	select {
	case data = <-input:
	case <-time.After(stdinTimeout):
	}
	if event, ok := hook.EventFor(cmd.Kind, data); ok {
		hook.Send(hook.Dir(), event)
	}
	return nil
}

func main() {
	var cli CLI
	kongCtx := kong.Parse(&cli, kong.Name("agent-bus"), kong.Vars{"version": version}, kong.Description("Adapter of the agent bus."))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// enroll bounds its own waiting (--waitforcert).
	if kongCtx.Command() != "mcp" && kongCtx.Command() != "enroll" && !cli.Pending.Wait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cli.Timeout)
		defer cancel()
	}

	kongCtx.BindTo(ctx, (*context.Context)(nil))
	kongCtx.Bind(&cli)
	if kongCtx.Command() != "pending" && kongCtx.Command() != "event" && kongCtx.Command() != "enroll" {
		session, closeSession, err := cli.open(kongCtx.Command() == "mcp")
		kongCtx.FatalIfErrorf(err)
		defer closeSession()
		kongCtx.Bind(session)
	}
	kongCtx.FatalIfErrorf(kongCtx.Run())
}

// open connects as the agent and takes the identity. closeSession releases both. A long-running
// session keeps reconnecting when the broker is down, also at start; a command fails at once.
func (cli *CLI) open(longRunning bool) (session *adapter.Adapter, closeSession func(), err error) {
	if cli.Agent == "" {
		return nil, nil, errors.New("--agent is required")
	}
	if err := bus.ValidateAgentID(cli.Agent); err != nil {
		return nil, nil, err
	}
	dir := creds.AgentDir(cli.ConfigDir, cli.Agent)
	caCert := cli.CACert
	if caCert == "" {
		caCert = filepath.Join(cli.ConfigDir, "ca.pem")
	}
	connection, err := broker.UserConnection(dir, bus.AgentToken(cli.Agent), cli.Broker, cli.MappedPort, caCert)
	if err != nil {
		return nil, nil, err
	}
	if longRunning {
		connection.Reconnect = &broker.Backoff{Initial: cli.ReconnectInitial, Max: cli.ReconnectMax}
		connection.Log = func(format string, args ...any) { log.Printf("agent-bus: "+format, args...) }
	}
	transport, err := broker.NewTransport(connection, cli.Agent)
	if err != nil {
		return nil, nil, err
	}
	session, err = adapter.Open(adapter.Config{
		AgentID: cli.Agent, Dir: dir, Transport: transport, Directory: trust.DirectoryPath(cli.ConfigDir), Groups: cli.Groups, Deliver: cli.Deliver, AutoAccept: cli.AutoAccept,
	})
	if err != nil {
		transport.Close()
		return nil, nil, err
	}
	// A command or session starts with the revocations the directory has now.
	if err := session.RefreshDirectory(context.Background()); err != nil && session.Ready() == nil {
		log.Printf("agent-bus: reading the directory: %v", err)
	}
	return session, func() { session.Close(); transport.Close() }, nil
}
