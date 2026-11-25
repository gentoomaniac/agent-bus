package enrollclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	// passwordDir holds the shared enrollment password on an agent's machine (`enroll/password`),
	// where `agent-busctl enroll init` keeps it on the central one.
	passwordDir = "enroll"
	// directoryTimeout bounds one fetch, the connection included: it runs while an agent sends or
	// reads mail, which must not wait long for a busd that is down.
	directoryTimeout = 10 * time.Second
)

// ErrNoEnrollPassword says this machine has no enrollment password to fetch the directory with.
var ErrNoEnrollPassword = errors.New("no enrollment password")

// DirectoryOptions say where to fetch the directory from and where it goes.
type DirectoryOptions struct {
	ConfigDir  string
	Broker     string // host:port of the password listener, where the enrollment login is
	CACertPath string
}

// FetchDirectory asks busd for the registry records and the channel catalogue and merges them into
// the machine's local directory. A running agent has no route to busd with its certificate, so it
// uses the shared enrollment login `agent-bus enroll` kept in enroll/password, or the owner put
// there for an agent enrolled before that. Each call connects anew: it runs once a minute at most.
func FetchDirectory(ctx context.Context, opts DirectoryOptions) error {
	password, err := creds.ReadPassword(filepath.Join(opts.ConfigDir, passwordDir))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoEnrollPassword
	}
	if err != nil {
		return err
	}
	if password == "" {
		return ErrNoEnrollPassword
	}
	ctx, cancel := context.WithTimeout(ctx, directoryTimeout)
	defer cancel()
	requester, err := newRequester(opts.Broker, opts.CACertPath, password, nil)
	if err != nil {
		return err
	}
	defer requester.Close()
	return mergeDirectory(ctx, requester, trust.DirectoryPath(opts.ConfigDir))
}

// mergeDirectory writes what busd answers into dir. It only adds and replaces: a record busd does
// not mention stays, and a record that says an agent was removed is how a removal travels.
func mergeDirectory(ctx context.Context, requester Requester, dir trust.FileDirectory) error {
	answer, err := requester.Request(ctx, enroll.SubjectDirectory, nil)
	if err != nil {
		return err
	}
	var reply enroll.DirectoryReply
	if err := json.Unmarshal(answer, &reply); err != nil {
		return fmt.Errorf("busd's answer is not a directory: %w", err)
	}
	if reply.Error != "" {
		return fmt.Errorf("busd cannot give the directory: %s", enroll.Printable(reply.Error))
	}
	// The login is shared, so anyone who has it can answer on this inbox: nothing that would name a
	// file outside the directory, or a group or reader the adapter would refuse, is written.
	for _, record := range reply.Records {
		if err := bus.ValidateAgentID(record.AgentID); err != nil {
			return fmt.Errorf("busd's directory has a bad record: %w", err)
		}
	}
	for name, readers := range reply.Channels {
		if err := bus.ValidateGroupName(name); err != nil {
			return fmt.Errorf("busd's directory has a bad channel: %w", err)
		}
		for _, reader := range readers {
			if err := bus.ValidateAgentID(reader); err != nil {
				return fmt.Errorf("busd's directory has a bad reader of %s: %w", name, err)
			}
		}
	}
	return writeDirectory(dir, reply.Directory)
}
