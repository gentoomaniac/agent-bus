package enrollclient

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

// brokerFile keeps the broker an identity enrolled at (host:port, as for --broker), so that
// `agent-bus mcp` without --broker connects there instead of to a default nobody chose. It lives
// in the identity's directory: one config directory can hold identities of different brokers, and
// the last enrollment must not repoint the others.
const brokerFile = "broker"

// BrokerPath is where enrolling keeps the broker's address for agentID.
func BrokerPath(configDir, agentID string) string {
	return filepath.Join(creds.AgentDir(configDir, agentID), brokerFile)
}

func writeBroker(configDir, agentID, address string) error {
	return files.Write(BrokerPath(configDir, agentID), []byte(address+"\n"), files.PublicMode, true)
}

// EnrolledBroker returns the broker agentID enrolled at, or "" if none is recorded. A file that is
// empty or not a host:port is an error: dialing something else would be silent.
func EnrolledBroker(configDir, agentID string) (string, error) {
	path := BrokerPath(configDir, agentID)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(string(data))
	if address == "" {
		return "", fmt.Errorf("%s is empty: write the broker's host:port into it, or delete it and pass --broker", path)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return "", fmt.Errorf("%s: %q is not host:port: %w", path, address, err)
	}
	return address, nil
}
