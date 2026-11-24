package bus

import (
	"context"
	"errors"
)

// ErrUnavailable is the error of a call that needs the broker while the connection to it is down
// and being re-established.
var ErrUnavailable = errors.New("bus unavailable, reconnecting")

// Transport is one agent's message path to the broker (design.md §2): publish to a channel,
// consume channels with explicit acknowledgement, and read history without touching a read cursor.
// It speaks in channels and agents, never in broker subjects or access rules.
type Transport interface {
	// Publish stores a record on a channel and returns the sequence the broker gave it. A retry
	// with the same id on the same channel inside the broker's window is stored only once and
	// returns the first sequence.
	Publish(ctx context.Context, to Channel, id string, payload []byte) (sequence uint64, err error)
	// Fetch returns up to max unacknowledged records from the agent's read cursor on a channel,
	// waiting briefly for them. The cursor moves only when a delivery is acknowledged. A record
	// that cannot be read is skipped and reported in the error, next to the deliveries returned.
	Fetch(ctx context.Context, from Channel, max int) ([]Delivery, error)
	// History reads up to limit records of a channel after the given sequence, oldest first.
	// It does not move any read cursor. Until busd exists, only the admin's connection may use it.
	History(ctx context.Context, from Channel, afterSequence uint64, limit int) ([]Record, error)
	// PublishPresence replaces the agent's own presence record, which the broker keeps for
	// readers (design.md §7). An agent can write no one else's.
	PublishPresence(record []byte) error
	// Presence returns the last presence record of an agent, or nil if it never published one.
	Presence(ctx context.Context, agentID string) ([]byte, error)
	// Unavailable is nil while the broker is reachable, and an error wrapping ErrUnavailable while
	// the connection is down and being re-established. The three calls above return it then at
	// once instead of waiting for the broker.
	Unavailable() error
	Close()
}

// Record is a stored message with its place in the channel's canonical order (§4).
type Record struct {
	Sequence uint64
	Payload  []byte
}

// Delivery is a record handed to a reader that has to acknowledge it.
type Delivery struct {
	Record
	// Ack tells the broker the record is handled and returns once the broker has confirmed it.
	Ack func(ctx context.Context) error
	// InProgress tells the broker the record is still being worked on, so it does not redeliver
	// it while the reader holds it.
	InProgress func(ctx context.Context) error
}

// Admin is the administrator's path to the broker (design.md §2): channels, accounts and the read
// cursors an account owns. Its methods are safe to repeat unless they say otherwise.
type Admin interface {
	// Setup creates what the bus needs on an empty broker: the message log, the registry, the
	// catalogue and the membership ledger. Existing ones keep their data.
	Setup(ctx context.Context) error
	// PutAgentRecord writes the agent's registry entry, replacing an earlier one.
	PutAgentRecord(ctx context.Context, record AgentRecord) error
	// CreateAccount lets an agent connect with the password and gives it its access. It fails if
	// the account exists.
	CreateAccount(ctx context.Context, agentID, password string) error
	// AccountExists says whether CreateAccount has succeeded for the agent.
	AccountExists(ctx context.Context, agentID string) (bool, error)
	// DeleteAccount removes the agent's access; its connections are closed.
	DeleteAccount(ctx context.Context, agentID string) error
	// CreateGroupChannel adds the channel to the catalogue, replacing the description of an
	// existing one, and gives every agent access to it. It says whether the channel is new.
	CreateGroupChannel(ctx context.Context, name, description string) (created bool, err error)
	// CreateReadCursor makes the agent's read cursor on a channel, starting after the given
	// sequence (0: at the beginning). A cursor that exists is left as it is.
	CreateReadCursor(ctx context.Context, agentID string, channel Channel, afterSequence uint64) error
	// DeleteReadCursor removes the agent's read cursor on a channel.
	DeleteReadCursor(ctx context.Context, agentID string, channel Channel) error
	// Agents lists the registry: the record of every registered agent.
	Agents(ctx context.Context) ([]AgentRecord, error)
	// GroupChannels lists the catalogue: every group channel.
	GroupChannels(ctx context.Context) ([]TopicRecord, error)
	// ReadCursorHolders lists the agent IDs that have a read cursor on the channel, that is the
	// agents that read it. A DM channel has the one holder it is named after.
	ReadCursorHolders(ctx context.Context, channel Channel) ([]string, error)
	// Presence returns the last presence record of an agent, or nil if it never published one.
	Presence(ctx context.Context, agentID string) ([]byte, error)
	// WatchPresence calls handler with every presence record published from now on, until stop.
	WatchPresence(handler func(agentID string, record []byte)) (stop func(), err error)
	// LastSequence is the sequence of the newest record of all conversation channels together.
	LastSequence(ctx context.Context) (uint64, error)
	Close()
}
