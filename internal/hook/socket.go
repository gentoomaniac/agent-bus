// Package hook connects the harness's delivery hooks to the adapter of their own session (design.md
// §7). The MCP server and the hook are both started by the harness, so the adapter listens on a
// unix socket named after its parent process and the hook finds it by walking up its own ancestors.
package hook

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	NewMail = "new-mail" // mail not told yet (Stop hook)
	Summary = "summary"  // all unread mail (SessionStart hook)
	event   = "event"    // a presence event; the reply is empty
)

// Mail is one message as a hook shows it. The title is peer data and untrusted.
type Mail struct {
	From, Type, Title string
}

// Reply is what the adapter tells a hook.
type Reply struct {
	Mail   []Mail
	Unread int // all unread messages, told or not
}

type request struct {
	Op    string
	Event *presence.Event `json:",omitempty"`
}

// Session is the adapter and the presence publisher as the hooks see them.
type Session interface {
	NewMail() (fresh []adapter.Message, unread int, err error)
	UnreadSummary() ([]adapter.Message, error)
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
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go serve(connection, session)
		}
	}()
	return func() { listener.Close() }, nil // closing removes the socket file
}

func serve(connection net.Conn, session Session) {
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
	reply := Reply{Unread: unread}
	for _, message := range messages {
		reply.Mail = append(reply.Mail, Mail{From: message.Envelope.From, Type: message.Envelope.Type, Title: titleOf(message)})
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

// Send tells the adapter of this process's session about a presence event, and says whether it
// got there.
func Send(dir string, presenceEvent presence.Event) bool {
	_, ok := ask(dir, request{Op: event, Event: &presenceEvent}, eventTimeout)
	return ok
}

func ask(dir string, asked request, timeout time.Duration) (Reply, bool) {
	for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
		if reply, ok := askSocket(socketPath(dir, pid), asked, timeout); ok {
			return reply, true
		}
	}
	return Reply{}, false
}

func askSocket(path string, asked request, timeout time.Duration) (Reply, bool) {
	connection, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return Reply{}, false
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(timeout))
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
