// Package monitor is the backend of the bus monitor (docs/design/monitor-and-api.md): it reads every channel with
// the admin's credentials, verifies each message and keeps what it saw in memory for the web
// interface. It reads history only; it never touches an agent's read cursor.
package monitor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/presence"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	historyPageSize   = 200
	discoverInterval  = 5 * time.Second // how often to look for new agents and group channels
	emptyReadPause    = 200 * time.Millisecond
	failedReadPause   = 5 * time.Second
	registryRefreshAt = 2 * time.Second // the shortest time between registry reads caused by unknown senders
	subscriberBuffer  = 256
	unknownAgent      = "unknown" // stands for the sender of a record that names none; no agent ID lacks an '@'
)

// Verification is the verification result of a message.
type Verification string

const (
	Verified      Verification = "verified"
	UnknownSender Verification = "unknown_sender" // the registry has no record of the sender, so nothing was checked
	Rejected      Verification = "rejected"
)

// Message is a stored record as the monitor shows it. A record that is not a valid envelope has
// only its sequence, channel, status and reason.
type Message struct {
	Sequence uint64       `json:"sequence"`
	Channel  string       `json:"channel"`
	Status   Verification `json:"status"`
	Reason   string       `json:"reason,omitempty"`
	ID       string       `json:"id,omitempty"`
	From     string       `json:"from,omitempty"`
	To       string       `json:"to,omitempty"`
	Type     string       `json:"type,omitempty"`
	Targets  []string     `json:"targets,omitempty"`
	Thread   string       `json:"thread,omitempty"`
	ReplyTo  *string      `json:"reply_to,omitempty"`
	Time     time.Time    `json:"time,omitzero"`
	Title    string       `json:"title,omitempty"`
	Text     string       `json:"text,omitempty"`
	Refs     []string     `json:"refs,omitempty"`

	// raw is the record as stored, for RawMessage. It doubles what the monitor holds per message in
	// memory (the parsed fields and the signed record), the price of serving it without a stream read.
	raw []byte
}

// Channel is a conversation of the bus as the monitor lists it. A DM channel is the conversation of
// two agents: both agents' DM subjects merged.
type Channel struct {
	ID           string   `json:"id"` // "general", "group:<name>" or "dm:<agent>:<agent>"
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Owner        string   `json:"owner,omitempty"`  // of a group channel an agent owns; else the bus owner's
	Agents       []string `json:"agents,omitempty"` // of a DM channel
	Messages     int      `json:"messages"`
	LastSequence uint64   `json:"last_sequence"`
}

// Agent is a registered agent. A removed one stays listed, marked, because its history remains.
type Agent struct {
	ID           string    `json:"id"`
	Fingerprint  string    `json:"fingerprint"`
	RegisteredAt time.Time `json:"registered_at"`
	Removed      bool      `json:"removed"`
}

// Config says what the monitor reads with. History must work on every channel, so it is a
// transport on the admin's connection.
type Config struct {
	Admin   bus.Admin
	History bus.Transport
	// Sender posts messages as one agent. Without it the monitor is read-only, and ReadOnlyNote,
	// if set, says why.
	Sender       Sender
	ReadOnlyNote string
	// Report is told about errors that do not stop the monitor, such as a failed read.
	Report func(error)
	// OfflineAfter is how long an agent may go without a heartbeat before it is shown offline;
	// zero means presence.OfflineAfter.
	OfflineAfter time.Duration
	// PrefsPath is the file the shared preferences (read markers, favourites, subscriptions, folded sections) are
	// kept in; empty keeps them in memory only.
	PrefsPath string
	// DirectoryHeads subscribes to busd's directory heads (broker.Admin.WatchDirectoryHeads): a
	// head that names a new version makes the monitor read the registry and the catalogue at once,
	// instead of at its next round. Nil: only the rounds. CACert (PEM) is what heads are checked
	// against.
	DirectoryHeads func(handler func(head []byte)) (stop func(), err error)
	CACert         []byte
}

// Monitor follows every channel and answers the web interface's questions.
type Monitor struct {
	config   Config
	registry *registry
	trust    *trust.Store

	mu         sync.Mutex
	groups     []bus.TopicRecord
	messages   map[string][]Message // by channel ID, ordered by sequence
	byID       map[string]position  // where each message with an ID is in messages
	followed   map[string]bool      // sources that have a reader, by channel name
	followers  sync.WaitGroup
	messageHub hub[Message]

	presenceMu    sync.Mutex
	records       map[string]presence.Record // last record by agent ID
	shownState    map[string]string          // state last announced, by agent ID
	askedPresence map[string]bool            // agents whose stored record was read at start
	presenceHub   hub[Presence]

	prefs *prefsStore

	pendingMu    sync.Mutex
	pendingCount int       // enrollment requests waiting, as busd announced
	pendingAt    time.Time // when busd announced it

	// headSeen asks Run for a round at once: a head named a new directory version.
	headSeen    chan struct{}
	directoryMu sync.Mutex
	directory   DirectoryStatus
}

// DirectoryStatus is how the monitor follows the directory, apart from presence: the last version
// busd announced, and why the last read of the registry or the catalogue failed, if it did.
type DirectoryStatus struct {
	Version string    `json:"version,omitempty"`
	HeadAt  time.Time `json:"head_at,omitzero"`
	Error   string    `json:"error,omitempty"`
}

func New(config Config) *Monitor {
	return &Monitor{
		config:        config,
		registry:      &registry{admin: config.Admin},
		trust:         trust.Unpinned(),
		messages:      map[string][]Message{},
		byID:          map[string]position{},
		followed:      map[string]bool{},
		records:       map[string]presence.Record{},
		shownState:    map[string]string{},
		askedPresence: map[string]bool{},
		prefs:         loadPrefs(config.PrefsPath, config.Report),
		headSeen:      make(chan struct{}, 1),
	}
}

// Run follows the bus until the context ends: one reader for general, each group channel and each
// agent's DM channel, started as they appear.
func (monitor *Monitor) Run(ctx context.Context) {
	defer monitor.followers.Wait()
	monitor.followers.Go(func() { monitor.followPresence(ctx) })
	monitor.followers.Go(func() { monitor.followPendingEnrollments(ctx) })
	if monitor.config.DirectoryHeads != nil {
		monitor.followers.Go(func() { monitor.followDirectoryHeads(ctx) })
	}
	for {
		err := monitor.discover(ctx)
		if err != nil && ctx.Err() == nil {
			monitor.report(err)
		}
		monitor.directoryMu.Lock()
		monitor.directory.Error = ""
		if err != nil && !errors.Is(err, bus.ErrUnavailable) {
			monitor.directory.Error = err.Error()
		}
		monitor.directoryMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(discoverInterval):
		case <-monitor.headSeen:
		}
	}
}

// followDirectoryHeads takes busd's heads until ctx ends. Only a head signed by the CA counts, and
// only as a cue: the agents and channels still come from the registry and the catalogue.
func (monitor *Monitor) followDirectoryHeads(ctx context.Context) {
	for ctx.Err() == nil {
		stop, err := monitor.config.DirectoryHeads(func(message []byte) {
			head, err := directory.VerifyHead(monitor.config.CACert, message)
			if err != nil {
				return
			}
			monitor.directoryMu.Lock()
			changed := head.Version.String() != monitor.directory.Version
			monitor.directory.Version, monitor.directory.HeadAt = head.Version.String(), time.Now()
			monitor.directoryMu.Unlock()
			if changed {
				select {
				case monitor.headSeen <- struct{}{}:
				default:
				}
			}
		})
		if err == nil {
			defer stop()
			<-ctx.Done()
			return
		}
		monitor.report(err)
		sleep(ctx, failedReadPause)
	}
}

// Directory is the monitor's view of the directory, for the status.
func (monitor *Monitor) Directory() DirectoryStatus {
	monitor.directoryMu.Lock()
	defer monitor.directoryMu.Unlock()
	return monitor.directory
}

// report passes on an error unless it is the bus being unavailable: the connection tells of that
// itself, once, and the readers retry every few seconds.
func (monitor *Monitor) report(err error) {
	if !errors.Is(err, bus.ErrUnavailable) {
		monitor.config.Report(err)
	}
}

func (monitor *Monitor) discover(ctx context.Context) error {
	if err := monitor.config.History.Unavailable(); err != nil {
		return err
	}
	agents, registryErr := monitor.registry.refresh(ctx)
	groups, groupsErr := monitor.config.Admin.GroupChannels(ctx)

	sources := []bus.Channel{bus.GeneralChannel()}
	for _, group := range groups {
		sources = append(sources, bus.GroupChannel(group.Name))
	}
	for _, agent := range agents {
		sources = append(sources, bus.DMChannel(agent.AgentID))
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if groupsErr == nil {
		monitor.groups = groups
	}
	for _, source := range sources {
		if monitor.followed[source.String()] {
			continue
		}
		monitor.followed[source.String()] = true
		monitor.followers.Go(func() { monitor.follow(ctx, source) })
	}
	return errors.Join(registryErr, groupsErr)
}

// follow reads the history of one source from its beginning and then waits for new records.
func (monitor *Monitor) follow(ctx context.Context, source bus.Channel) {
	var after uint64
	for ctx.Err() == nil {
		records, err := monitor.config.History.History(ctx, source, after, historyPageSize)
		if err != nil {
			if ctx.Err() == nil {
				monitor.report(err)
			}
			sleep(ctx, failedReadPause)
			continue
		}
		for _, record := range records {
			monitor.add(monitor.interpret(ctx, source, record))
			after = record.Sequence
		}
		if len(records) == 0 {
			sleep(ctx, emptyReadPause)
		}
	}
}

func sleep(ctx context.Context, duration time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}

// interpret verifies a record as the adapter of the reader of the source would (docs/design/identity-and-trust.md), except for
// timestamps and duplicates, and trusting the registry alone.
func (monitor *Monitor) interpret(ctx context.Context, source bus.Channel, record bus.Record) Message {
	message := Message{Sequence: record.Sequence, Channel: channelID(source, unknownAgent), Status: Rejected, raw: record.Payload}
	signed, err := envelope.Parse(record.Payload)
	if err != nil {
		message.Reason = err.Error()
		return message
	}
	env := signed.Envelope
	message.ID, message.From, message.To, message.Type, message.Targets = env.ID, env.From, env.To.Name, env.Type, env.Targets
	message.Thread, message.ReplyTo, message.Time = env.Thread, env.ReplyTo, env.TS
	message.Title, message.Text, message.Refs = env.Body.Title, env.Body.Text, env.Body.Refs

	if err := bus.ValidateAgentID(env.From); err != nil {
		message.Reason = err.Error()
		return message
	}
	message.Channel = channelID(source, env.From)
	if err := env.CheckTo(source); err != nil {
		message.Reason = err.Error()
		return message
	}
	resolution, err := monitor.trust.Resolve(ctx, monitor.registry, env.From, env.KeyFP, time.Now())
	switch {
	case errors.Is(err, trust.ErrUnavailable):
		message.Status, message.Reason = UnknownSender, err.Error()
	case err != nil:
		message.Reason = err.Error()
	case !signed.Verify(resolution.Key):
		message.Reason = fmt.Sprintf("signature of %s does not verify", env.From)
	default:
		message.Status = Verified
	}
	return message
}

// channelID names the conversation a record of the source belongs to. For a DM source, sender is
// the other side of the conversation.
func channelID(source bus.Channel, sender string) string {
	switch source.Kind {
	case bus.General:
		return "general"
	case bus.Group:
		return "group:" + source.Name
	default:
		pair := []string{sender, source.Name}
		slices.Sort(pair)
		return "dm:" + strings.Join(pair, ":")
	}
}

// add stores a message in its channel's order and tells the subscribers.
func (monitor *Monitor) add(message Message) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	list := monitor.messages[message.Channel]
	index, found := slices.BinarySearchFunc(list, message.Sequence, compareSequence)
	if found {
		return
	}
	monitor.messages[message.Channel] = slices.Insert(list, index, message)
	if message.ID != "" {
		monitor.byID[message.ID] = position{message.Channel, message.Sequence}
	}
	monitor.messageHub.publish(message)
}

func compareSequence(stored Message, sequence uint64) int {
	return cmp.Compare(stored.Sequence, sequence)
}

// Subscribe returns the messages added from now on. The channel is closed when the subscriber
// falls behind; unsubscribe is safe to call then.
func (monitor *Monitor) Subscribe() (messages <-chan Message, unsubscribe func()) {
	return monitor.messageHub.subscribe()
}

// Channels lists general, the catalogued group channels and the DM conversations seen so far.
func (monitor *Monitor) Channels() []Channel {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	describe := func(channel Channel) Channel {
		list := monitor.messages[channel.ID]
		channel.Messages = len(list)
		if len(list) > 0 {
			channel.LastSequence = list[len(list)-1].Sequence
		}
		return channel
	}
	channels := []Channel{describe(Channel{ID: "general", Kind: "general", Name: "general"})}
	groups := slices.SortedFunc(slices.Values(monitor.groups), func(a, b bus.TopicRecord) int { return cmp.Compare(a.Name, b.Name) })
	for _, group := range groups {
		channels = append(channels, describe(Channel{ID: "group:" + group.Name, Kind: "group", Name: group.Name, Description: group.Description, Owner: group.Owner}))
	}
	for _, id := range slices.Sorted(maps.Keys(monitor.messages)) {
		if agents, isDM := dmAgents(id); isDM {
			channels = append(channels, describe(Channel{ID: id, Kind: "dm", Name: strings.Join(agents, " + "), Agents: agents}))
		}
	}
	return channels
}

// dmAgents splits the ID of a DM channel into its two agents.
func dmAgents(id string) ([]string, bool) {
	rest, isDM := strings.CutPrefix(id, "dm:")
	agents := strings.Split(rest, ":")
	return agents, isDM && len(agents) == 2
}

// Status says what the monitor can do besides reading.
func (monitor *Monitor) Status() Status {
	busState := BusLive
	if monitor.config.History.Unavailable() != nil {
		busState = BusReconnecting
	}
	sender := monitor.config.Sender
	if sender == nil {
		return Status{Bus: busState, PendingEnrollments: monitor.pendingEnrollments(), Directory: monitor.Directory(),
			Note: cmp.Or(monitor.config.ReadOnlyNote, "read-only: the monitor was started without --agent")}
	}
	status := Status{Bus: busState, Agent: sender.AgentID(), PendingEnrollments: monitor.pendingEnrollments(), Directory: monitor.Directory()}
	if err := sender.Ready(); err != nil {
		status.Note = err.Error()
		return status
	}
	status.CanSend = true
	return status
}

// Agents lists the registered agents, removed ones included.
func (monitor *Monitor) Agents() []Agent {
	return monitor.registry.agents()
}

// ErrUnknownChannel is returned for a channel ID that names no channel.
var ErrUnknownChannel = errors.New("unknown channel")

// Members are the agents that take part in a channel: for general all agents, for a group channel
// the agents with a read cursor on it, for a DM channel its two agents.
func (monitor *Monitor) Members(ctx context.Context, id string) ([]string, error) {
	var members []string
	switch {
	case id == "general":
		for _, agent := range monitor.registry.agents() {
			if !agent.Removed {
				members = append(members, agent.ID)
			}
		}
	case strings.HasPrefix(id, "group:"):
		name := strings.TrimPrefix(id, "group:")
		if !monitor.hasGroup(name) {
			return nil, ErrUnknownChannel
		}
		holders, err := monitor.config.Admin.ReadCursorHolders(ctx, bus.GroupChannel(name))
		if err != nil {
			return nil, err
		}
		members = holders
	default:
		agents, isDM := dmAgents(id)
		if !isDM {
			return nil, ErrUnknownChannel
		}
		members = slices.DeleteFunc(agents, func(agent string) bool { return agent == unknownAgent })
	}
	slices.Sort(members)
	return members, nil
}

func (monitor *Monitor) hasGroup(name string) bool {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return slices.ContainsFunc(monitor.groups, func(group bus.TopicRecord) bool { return group.Name == name })
}

// Messages returns up to limit messages of the channel after the given sequence, oldest first.
func (monitor *Monitor) Messages(id string, after uint64, limit int) []Message {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	list := monitor.messages[id]
	start, found := slices.BinarySearchFunc(list, after, compareSequence)
	if found {
		start++
	}
	return slices.Clone(list[start:min(len(list), start+limit)])
}

// position finds a message in Monitor.messages by its channel and sequence, which do not change
// when later messages are inserted before it.
type position struct {
	channel  string
	sequence uint64
}

// MessageByID returns the message with the envelope ID, whatever channel it is in.
func (monitor *Monitor) MessageByID(id string) (Message, error) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	at, known := monitor.byID[id]
	if !known {
		return Message{}, ErrUnknownMessage
	}
	list := monitor.messages[at.channel]
	index, _ := slices.BinarySearchFunc(list, at.sequence, compareSequence)
	return list[index], nil
}

// ErrUnknownMessage is returned for a channel and sequence that name no stored message.
var ErrUnknownMessage = errors.New("unknown message")

// RawMessage returns the message as Messages shows it, and the record as stored. The monitor keeps
// the stored bytes with each message, as a DM channel merges two streams and has no single source
// to read a sequence from.
func (monitor *Monitor) RawMessage(id string, sequence uint64) (Message, []byte, error) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	list := monitor.messages[id]
	index, found := slices.BinarySearchFunc(list, sequence, compareSequence)
	if !found {
		return Message{}, nil, ErrUnknownMessage
	}
	return list[index], list[index].raw, nil
}

// registry is the registry of agents, read through the admin and cached. It is the trust.Directory
// the monitor verifies with.
type registry struct {
	admin bus.Admin

	mu          sync.Mutex
	records     map[string]bus.AgentRecord
	refreshedAt time.Time
}

func (registry *registry) refresh(ctx context.Context) ([]bus.AgentRecord, error) {
	records, err := registry.admin.Agents(ctx)
	if err != nil {
		return nil, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.records = map[string]bus.AgentRecord{}
	for _, record := range records {
		registry.records[record.AgentID] = record
	}
	registry.refreshedAt = time.Now()
	return records, nil
}

// Lookup serves from the cache, and reads the registry again when the agent is missing, so an
// agent registered a moment ago is not shown as unknown.
func (registry *registry) Lookup(ctx context.Context, agentID string) (bus.AgentRecord, error) {
	registry.mu.Lock()
	record, found := registry.records[agentID]
	stale := time.Since(registry.refreshedAt) >= registryRefreshAt
	registry.mu.Unlock()
	if !found && stale {
		if _, err := registry.refresh(ctx); err != nil {
			return bus.AgentRecord{}, err
		}
		registry.mu.Lock()
		record, found = registry.records[agentID]
		registry.mu.Unlock()
	}
	if !found {
		return record, fmt.Errorf("%s is not in the registry: %w", agentID, fs.ErrNotExist)
	}
	return record, nil
}

func (registry *registry) agents() []Agent {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var agents []Agent
	for _, record := range registry.records {
		agents = append(agents, Agent{ID: record.AgentID, Fingerprint: record.Fingerprint, RegisteredAt: record.RegisteredAt, Removed: record.Removed()})
	}
	slices.SortFunc(agents, func(a, b Agent) int { return cmp.Compare(a.ID, b.ID) })
	return agents
}

// pendingFresh is how long an announcement of the pending count counts: busd repeats it every few
// seconds, so a count that stops coming means busd is gone, and nobody can be waiting on it.
const pendingFresh = 30 * time.Second

// followPendingEnrollments keeps the count busd announces until ctx ends.
func (monitor *Monitor) followPendingEnrollments(ctx context.Context) {
	for ctx.Err() == nil {
		stop, err := monitor.config.Admin.WatchPendingEnrollments(func(count int) {
			monitor.pendingMu.Lock()
			defer monitor.pendingMu.Unlock()
			monitor.pendingCount, monitor.pendingAt = count, time.Now()
		})
		if err == nil {
			defer stop()
			<-ctx.Done()
			return
		}
		monitor.report(err)
		sleep(ctx, failedReadPause)
	}
}

func (monitor *Monitor) pendingEnrollments() int {
	monitor.pendingMu.Lock()
	defer monitor.pendingMu.Unlock()
	if time.Since(monitor.pendingAt) > pendingFresh {
		return 0
	}
	return monitor.pendingCount
}
