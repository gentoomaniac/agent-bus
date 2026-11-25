package hook

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// A hook that finds no socket cannot tell a session that never had an MCP server from one whose
// server died, and the harness does not say. So a listening adapter leaves a marker next to its
// socket, and keeps it after it ends: a hook that finds the marker but no one behind the socket
// says so once, and takes the marker away. The next adapter of the harness (after /mcp) makes a new one.

const (
	markerSuffix = ".session"

	lostServerNotice = "this session's MCP server (agent-bus mcp) is not running, so mail is neither reported nor readable. " +
		"Tell your owner to run /mcp: Claude Code does not restart a stdio server by itself."
)

func markerPath(dir string, harnessPID int) string {
	return filepath.Join(dir, strconv.Itoa(harnessPID)+markerSuffix)
}

// markSession records that the harness has an MCP server. It first clears the markers of harnesses
// that ended, which nobody will ever ask about.
func markSession(dir string, harnessPID int) error {
	markers, _ := filepath.Glob(filepath.Join(dir, "*"+markerSuffix))
	for _, marker := range markers {
		pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(marker), markerSuffix))
		if err == nil && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			os.Remove(marker)
		}
	}
	return os.WriteFile(markerPath(dir, harnessPID), nil, 0o600)
}

// LostServer returns the notice that this process's session lost its MCP server, once: the first
// call after the loss takes the marker away, and later ones return "". It returns "" for a session
// that never had a server, and for one whose server still accepts connections, however slow.
func LostServer(dir string) string {
	for pid := os.Getppid(); pid > 1; pid = parentOf(pid) {
		if _, err := os.Stat(markerPath(dir, pid)); err != nil {
			continue
		}
		if connection, err := net.DialTimeout("unix", socketPath(dir, pid), connectionTimeout); err == nil {
			connection.Close()
			return ""
		}
		// Of several hooks that find the loss together, the one that removes the marker tells.
		if os.Remove(markerPath(dir, pid)) == nil {
			return lostServerNotice
		}
	}
	return ""
}
