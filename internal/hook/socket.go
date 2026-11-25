// Package hook connects the harness's delivery hooks to the adapter of their own session (docs/design/delivery.md). The MCP server and the hook are both started by the harness, so the adapter listens on a
// unix socket named after its parent process (past any wrappers) and the hook finds it by walking up its own ancestors.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
)

const (
	// connectionTimeout bounds one request on either side: a hook must never hang its session.
	connectionTimeout = 2 * time.Second
	// eventTimeout is shorter: an event hook runs on every tool call and must not slow the session,
	// and SessionEnd hooks get 1.5 seconds in all.
	eventTimeout = 500 * time.Millisecond

	// Requests: what a hook asks of the adapter.
	NewMail  = "new-mail"  // mail not told yet (Stop hook)
	PeekMail = "peek-mail" // the same, but not marked told until MarkTold
	WaitMail = "wait-mail" // blocks until there is new mail (the idle waiter); an empty reply means it ended
	Summary  = "summary"   // all unread mail (SessionStart hook)
	MarkTold = "mark-told" // mark messages told after the notice was delivered; the reply is empty
	event    = "event"     // a presence event; the reply is empty
)

// Mail is one message as a hook shows it. The title is peer data and untrusted.
type Mail struct {
	ID, From, Type, Title string
}

// Reply is what the adapter tells a hook.
type Reply struct {
	Mail   []Mail
	Unread int // all unread messages, told or not
	// Problem is a sentence about the session's link to the bus (the MCP server is gone, the broker
	// is out of reach), for the agent to pass on to its owner. It is empty when all is well.
	Problem string
	// Directory are the adapter's own notices of directory changes. They ride along with mail and
	// the session start's summary: on its own a notice never wakes or holds up a session.
	Directory []string `json:",omitempty"`
}

type request struct {
	Op    string
	Event *presence.Event `json:",omitempty"`
	IDs   []string        `json:",omitempty"` // for MarkTold
}

// Session is the adapter and the presence publisher as the hooks see them.
type Session interface {
	NewMail() (fresh []adapter.Message, unread int, err error)
	WaitNewMail(ctx context.Context) (fresh []adapter.Message, unread int, err error)
	UnreadSummary() ([]adapter.Message, error)
	PeekNewMail() (fresh []adapter.Message, unread int, err error)
	MarkNotified(ids []string)
	TakeDirectoryNotices() []string
	Event(presence.Event)
	// BusUnavailable is why the adapter cannot reach the broker, or nil.
	BusUnavailable() error
}

// Dir is the private directory of the sockets: $XDG_RUNTIME_DIR/agent-bus, or a per-user one in
// the temporary directory.
func Dir() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "agent-bus")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-bus-%d", os.Getuid()))
}

func socketPath(dir string, harnessPID int) string {
	return filepath.Join(dir, strconv.Itoa(harnessPID)+".sock")
}

// Listen serves the hook requests of this process's session, on a socket named after the harness
// (the parent process, past any wrappers), until stop is called. A socket left by a crashed
// process of the same name is replaced.
func Listen(dir string, session Session) (stop func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	harness := harnessPID(os.Getppid(), readProcess)
	log.Printf("agent-bus: hook socket named after harness PID %d (parent %d)", harness, os.Getppid())
	path := socketPath(dir, harness)
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := markSession(dir, harness); err != nil {
		log.Printf("agent-bus: no session marker, a lost server will go unreported: %v", err)
	}
	var waiter currentWaiter
	var brokerOutage outage
	go acceptLoop(listener, func(connection net.Conn) { serve(connection, session, &waiter, &brokerOutage) })
	return func() { listener.Close() }, nil // closing removes the socket file
}

// acceptRetryDelay is the pause after a failed accept, such as "too many open files", so that a
// persistent failure is logged once a second instead of spinning.
const acceptRetryDelay = time.Second

// acceptLoop serves each connection on its own goroutine until the listener is closed. Any other
// accept error is transient: giving up on it would leave the process alive with a dead socket, and
// every hook of the session silent.
func acceptLoop(listener net.Listener, serveConnection func(net.Conn)) {
	for {
		connection, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			log.Printf("agent-bus: accepting on the hook socket: %v", err)
			time.Sleep(acceptRetryDelay)
			continue
		}
		go serveConnection(connection)
	}
}

// currentWaiter holds the one idle waiter a session has: a new one replaces the old, which then
// ends with an empty reply. The hook entries re-arm the waiter at every turn and every start, and
// the old ones would only pile up.
type currentWaiter struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (waiter *currentWaiter) replace(cancel context.CancelFunc) {
	waiter.mu.Lock()
	defer waiter.mu.Unlock()
	if waiter.cancel != nil {
		waiter.cancel()
	}
	waiter.cancel = cancel
}

func serve(connection net.Conn, session Session, waiter *currentWaiter, brokerOutage *outage) {
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(connectionTimeout))
	var asked request
	if json.NewDecoder(connection).Decode(&asked) != nil {
		return
	}
	var messages []adapter.Message
	var unread int
	var err error
	switch asked.Op {
	case NewMail:
		messages, unread, err = session.NewMail()
	case PeekMail:
		messages, unread, err = session.PeekNewMail()
	case WaitMail:
		serveWait(connection, session, waiter, brokerOutage)
		return
	case MarkTold:
		session.MarkNotified(asked.IDs)
		json.NewEncoder(connection).Encode(Reply{})
		return
	case Summary:
		messages, err = session.UnreadSummary()
		unread = len(messages)
	case event:
		if asked.Event != nil {
			session.Event(*asked.Event)
		}
		json.NewEncoder(connection).Encode(Reply{})
		return
	default:
		return
	}
	if err != nil { // no lock: the session's hooks stay silent
		return
	}
	reply := replyFor(messages, unread)
	if asked.Op != PeekMail { // the plugin's output has no room for it
		reply.Problem = brokerOutage.report(session.BusUnavailable(), time.Now())
	}
	if len(messages) > 0 || asked.Op == Summary {
		reply.Directory = session.TakeDirectoryNotices()
	}
	json.NewEncoder(connection).Encode(reply)
}

func replyFor(messages []adapter.Message, unread int) Reply {
	reply := Reply{Unread: unread}
	for _, message := range messages {
		reply.Mail = append(reply.Mail, Mail{ID: message.Envelope.ID, From: message.Envelope.From, Type: message.Envelope.Type, Title: titleOf(message)})
	}
	return reply
}

// serveWait answers with the mail that ends the wait, or with an empty reply when the waiter was
// replaced. It also ends the wait to report an outage of the bus, which nothing else would wake an
// idle session for. It stops waiting when the client goes away, so nothing is marked told for nobody.
func serveWait(connection net.Conn, session Session, waiter *currentWaiter, brokerOutage *outage) {
	connection.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter.replace(cancel)
	go func() {
		io.Copy(io.Discard, connection) // the client sends nothing more: this returns when it closes
		cancel()
	}()

	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	outageReport, watchEnded := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(watchEnded)
		ticker := time.NewTicker(outagePoll)
		defer ticker.Stop()
		for {
			select {
			case <-waitCtx.Done():
				return
			case <-ticker.C:
				if problem := brokerOutage.report(session.BusUnavailable(), time.Now()); problem != "" {
					outageReport <- problem
					stopWaiting()
					return
				}
			}
		}
	}()
	messages, unread, err := session.WaitNewMail(waitCtx)
	if err != nil {
		messages, unread = nil, 0
	}
	stopWaiting()
	<-watchEnded // a report it made is not lost to a mail that arrived at the same moment
	reply := replyFor(messages, unread)
	if len(messages) > 0 {
		reply.Directory = session.TakeDirectoryNotices()
	}
	select {
	case reply.Problem = <-outageReport:
	default:
	}
	json.NewEncoder(connection).Encode(reply)
}

// titleOf is the message's title, or the first line of its text.
func titleOf(message adapter.Message) string {
	if title := message.Envelope.Body.Title; title != "" {
		return title
	}
	firstLine, _, _ := strings.Cut(message.Envelope.Body.Text, "\n")
	return firstLine
}

// Ask puts the request to the adapter of this process's session: the nearest ancestor with a
// socket in dir. It returns false when there is none or it does not answer.
func Ask(dir, op string) (Reply, bool) {
	return ask(dir, request{Op: op}, connectionTimeout)
}

// Wait blocks until the adapter of this process's session has new mail for it, and returns that.
// The reply has no mail when the wait ended otherwise: max went by, the waiter was replaced by a
// newer one, ctx ended, or there is no adapter. A session whose adapter went away gets a Problem.
func Wait(ctx context.Context, dir string, max time.Duration) Reply {
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	// At SessionStart the hook can run before the session's MCP server has opened its socket, so
	// keep looking for it for a while instead of giving up at once.
	lookUntil := time.Now().Add(socketStartupWait)
	for {
		reply, ok := askUntil(ctx, dir, request{Op: WaitMail})
		if ok || ctx.Err() != nil {
			return reply
		}
		if problem := LostServer(dir); problem != "" {
			return Reply{Problem: problem}
		}
		if time.Now().After(lookUntil) {
			return reply
		}
		select {
		case <-ctx.Done():
			return Reply{}
		case <-time.After(time.Second):
		}
	}
}

// socketStartupWait is how long Wait keeps looking for a session's socket that is not there yet.
const socketStartupWait = 2 * time.Minute

// ConfirmTold tells the adapter of this process's session that the notice for these messages was
// delivered, and says whether it got there.
func ConfirmTold(dir string, ids []string) bool {
	_, ok := ask(dir, request{Op: MarkTold, IDs: ids}, connectionTimeout)
	return ok
}

// Send tells the adapter of this process's session about a presence event, and says whether it
// got there.
func Send(dir string, presenceEvent presence.Event) bool {
	_, ok := ask(dir, request{Op: event, Event: &presenceEvent}, eventTimeout)
	return ok
}

func ask(dir string, asked request, timeout time.Duration) (Reply, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return askUntil(ctx, dir, asked)
}

// askUntil puts the request to the nearest ancestor with a socket and waits for its reply until
// ctx ends.
func askUntil(ctx context.Context, dir string, asked request) (Reply, bool) {
	for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
		if reply, ok := askSocket(ctx, socketPath(dir, pid), asked); ok {
			return reply, true
		}
	}
	return Reply{}, false
}

func askSocket(ctx context.Context, path string, asked request) (Reply, bool) {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Reply{}, false
	}
	defer connection.Close()
	// Closing the connection is what ends a read that waits for the reply.
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	var reply Reply
	if json.NewEncoder(connection).Encode(asked) != nil || json.NewDecoder(connection).Decode(&reply) != nil {
		return Reply{}, false
	}
	return reply, true
}

// wrappers are the processes that only start another one, by their name in /proc. An MCP config
// that starts agent-bus through one of them makes the wrapper its parent, which no hook has among
// its ancestors.
var wrappers = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "fish": true,
	"env": true, "mise": true, "direnv": true, "nice": true, "timeout": true,
	"nohup": true, "stdbuf": true, "setsid": true,
}

// harnessPID is the first ancestor from pid up that is not a wrapper. It stops at the wrapper itself
// when the walk would reach PID 1 or fail.
func harnessPID(pid int, read func(pid int) (name string, parent int)) int {
	// A real process tree has no cycles; the bound only guards against a broken /proc reader.
	for depth := 0; depth < 64; depth++ {
		name, parent := read(pid)
		if !wrappers[name] || parent <= 1 {
			return pid
		}
		pid = parent
	}
	return pid
}

// readProcess reads the name and the parent PID of a process from /proc (Linux); a parent of 0
// ends the walk.
func readProcess(pid int) (name string, parent int) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", 0
	}
	// The command name in parentheses may contain spaces and parentheses; the fields after the last
	// closing one are fixed.
	text := string(stat)
	open, close := strings.Index(text, "("), strings.LastIndex(text, ")")
	if open < 0 || close < open {
		return "", 0
	}
	fields := strings.Fields(text[close+1:])
	if len(fields) < 2 {
		return "", 0
	}
	parent, _ = strconv.Atoi(fields[1])
	return text[open+1 : close], parent
}

func parentOf(pid int) int {
	_, parent := readProcess(pid)
	return parent
}
