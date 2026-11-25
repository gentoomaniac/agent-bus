// Package hook connects the harness's delivery hooks to the adapter of their own session (docs/design/delivery.md). The MCP server and the hook are both started by the harness, so the adapter listens on a
// unix socket named after its parent process and the hook finds it by walking up its own ancestors.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Event(presence.Event)
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

// Listen serves the hook requests of this process's session, on a socket named after the parent
// process, until stop is called. A socket left by a crashed process of the same name is replaced.
func Listen(dir string, session Session) (stop func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	path := socketPath(dir, os.Getppid())
	os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	var waiter currentWaiter
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(connection, session, &waiter)
		}
	}()
	return func() { listener.Close() }, nil // closing removes the socket file
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

func serve(connection net.Conn, session Session, waiter *currentWaiter) {
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
		serveWait(connection, session, waiter)
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
	json.NewEncoder(connection).Encode(replyFor(messages, unread))
}

func replyFor(messages []adapter.Message, unread int) Reply {
	reply := Reply{Unread: unread}
	for _, message := range messages {
		reply.Mail = append(reply.Mail, Mail{ID: message.Envelope.ID, From: message.Envelope.From, Type: message.Envelope.Type, Title: titleOf(message)})
	}
	return reply
}

// serveWait answers with the mail that ends the wait, or with an empty reply when the waiter was
// replaced. It stops waiting when the client goes away, so nothing is marked told for nobody.
func serveWait(connection net.Conn, session Session, waiter *currentWaiter) {
	connection.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter.replace(cancel)
	go func() {
		io.Copy(io.Discard, connection) // the client sends nothing more: this returns when it closes
		cancel()
	}()
	messages, unread, err := session.WaitNewMail(ctx)
	if err != nil {
		messages, unread = nil, 0
	}
	json.NewEncoder(connection).Encode(replyFor(messages, unread))
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
// newer one, ctx ended, or there is no adapter.
func Wait(ctx context.Context, dir string, max time.Duration) Reply {
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	// At SessionStart the hook can run before the session's MCP server has opened its socket, so
	// keep looking for it for a while instead of giving up at once.
	lookUntil := time.Now().Add(socketStartupWait)
	for {
		reply, ok := askUntil(ctx, dir, request{Op: WaitMail})
		if ok || ctx.Err() != nil || time.Now().After(lookUntil) {
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

// parentOf reads the parent PID from /proc (Linux); 0 ends the walk.
func parentOf(pid int) int {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The command name in parentheses may contain spaces; the fields after it are fixed.
	_, afterName, found := strings.Cut(string(stat), ") ")
	fields := strings.Fields(afterName)
	if !found || len(fields) < 2 {
		return 0
	}
	parent, _ := strconv.Atoi(fields[1])
	return parent
}
