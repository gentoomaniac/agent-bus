package adapter

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/envelope"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	defaultRateLimit = 30 // messages per rateWindow (§8 rule 4)
	rateWindow       = 10 * time.Minute
	defaultMaxHops   = 6 // §8 rule 3

	// recentParentWindow is how long a delivered or acked message binds a send that names no
	// reply_to (§8 rule 3).
	recentParentWindow = 5 * time.Minute
	keepAliveInterval  = 20 * time.Second // well inside the consumers' one-minute AckWait
	idlePause          = time.Second      // between rounds of Run that fetched nothing

	// DeliverTargeted keeps untargeted channel messages out of the inbox; DeliverAll delivers every
	// message (design.md §7).
	DeliverTargeted = "targeted"
	DeliverAll      = "all"

	knownAgentsFile = "known_agents"
	handledFile     = "handled.jsonl"
	alertsFile      = "alerts.log"
)

// Config says who the adapter is and what it talks to.
type Config struct {
	AgentID   string
	Dir       string // the identity's directory: key, lock, known_agents, handled.jsonl, alerts.log
	Transport bus.Transport
	Directory trust.Directory
	// Groups are group channels to read besides the agent's DM channel, general and the groups the
	// directory's catalogue snapshot lists the agent in (written by `busctl channel join`). Their
	// read cursors must exist (busctl creates them).
	Groups []string

	// AutoAccept says the agent may act on a request aimed at it without asking its owner
	// (design.md §8 rule 2). It only changes what the MCP server tells the agent.
	AutoAccept bool

	Deliver       string // DeliverTargeted (default) or DeliverAll
	InboxMessages int    // default DefaultInboxMessages
	InboxBytes    int    // default DefaultInboxBytes
	// ContextMessages caps the mail that does not target the agent (--deliver=all); default
	// DefaultContextMessages.
	ContextMessages int
	RateLimit       int // sends per 10 minutes, default 30
	MaxHops         int // default 6
	Now             func() time.Time
}

// Outgoing is what the agent chooses about a message; the adapter fills in the rest.
type Outgoing struct {
	Type    string
	Body    envelope.Body
	Targets []string // agent IDs or "@all"; default: see targetsFor
	Thread  string   // default: the parent's thread, or a new one
	ReplyTo string   // default: the message the agent most recently read or acked (§8 rule 3)
}

// InboxQuery narrows what Inbox returns; zero values select everything.
type InboxQuery struct {
	ID      string
	Limit   int
	Types   []string
	Channel *bus.Channel
	// NewOnly leaves out the messages an earlier Inbox call already listed.
	NewOnly bool
	// Peek lists without counting as the agent having seen the messages: they are not marked
	// returned and do not become the parent of its next reply.
	Peek bool
}

// Adapter is one agent's session on the bus.
type Adapter struct {
	config Config
	// lockErr is set when another process holds the identity: the adapter still starts, but every
	// operation returns it (§3).
	lockErr error
	lock    *identity.Lock

	key     ed25519.PrivateKey
	keyFP   string
	trust   *trust.Store
	handled *handledSet
	// alertsPath is a plain log until busctl alerts exists.
	alertsPath string
	channels   []bus.Channel

	pollMu sync.Mutex // one reader at a time; fetching must not hold mu
	mu     sync.Mutex // guards everything below and the stores above
	inbox  inbox
	rate   rateLimiter

	notifications notifications
	// contextDropped counts the context messages dropped for room since TakeContextDropped.
	contextDropped int
	acked          map[string]bool // IDs of the messages this agent sent an ack reply to
	// unknownSenders maps a sender with no directory record to the deliveries (destination and
	// sequence) of its messages waiting for one.
	unknownSenders map[string]map[string]bool
	recent         struct {
		id string
		at time.Time
	}
}

// Open takes the identity. If another process already holds it, the returned adapter is inert:
// every operation fails with an error naming the holder.
func Open(config Config) (*Adapter, error) {
	if err := bus.ValidateAgentID(config.AgentID); err != nil {
		return nil, err
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	config.Deliver = cmp.Or(config.Deliver, DeliverTargeted)
	if config.Deliver != DeliverTargeted && config.Deliver != DeliverAll {
		return nil, fmt.Errorf("unknown delivery mode %q, want %s or %s", config.Deliver, DeliverTargeted, DeliverAll)
	}
	config.InboxMessages = cmp.Or(config.InboxMessages, DefaultInboxMessages)
	config.InboxBytes = cmp.Or(config.InboxBytes, DefaultInboxBytes)
	config.ContextMessages = cmp.Or(config.ContextMessages, DefaultContextMessages)
	config.RateLimit = cmp.Or(config.RateLimit, defaultRateLimit)
	config.MaxHops = cmp.Or(config.MaxHops, defaultMaxHops)
	adapter := &Adapter{config: config}

	lock, err := identity.AcquireLock(config.Dir)
	var held identity.HeldError
	if errors.As(err, &held) {
		adapter.lockErr = fmt.Errorf("%s: %w", config.AgentID, err)
		return adapter, nil
	}
	if err != nil {
		return nil, err
	}
	adapter.lock = lock
	if err := adapter.load(); err != nil {
		lock.Release()
		return nil, err
	}
	return adapter, nil
}

func (adapter *Adapter) load() (err error) {
	config := adapter.config
	if adapter.key, err = identity.ReadPrivateKey(config.Dir); err != nil {
		return err
	}
	adapter.keyFP = identity.Fingerprint(adapter.key.Public().(ed25519.PublicKey))
	if adapter.trust, err = trust.Open(filepath.Join(config.Dir, knownAgentsFile)); err != nil {
		return err
	}
	if adapter.handled, err = openHandledSet(filepath.Join(config.Dir, handledFile)); err != nil {
		return err
	}
	adapter.alertsPath = filepath.Join(config.Dir, alertsFile)
	adapter.inbox = inbox{maxMessages: config.InboxMessages, maxBytes: config.InboxBytes, maxContext: config.ContextMessages}
	adapter.rate = rateLimiter{limit: config.RateLimit, window: rateWindow}
	catalogue, err := adapter.catalogue()
	if err != nil {
		return err
	}
	groups := slices.Clone(config.Groups)
	for name, readers := range catalogue {
		if slices.Contains(readers, config.AgentID) {
			groups = append(groups, name)
		}
	}
	slices.Sort(groups)
	adapter.channels = []bus.Channel{bus.DMChannel(config.AgentID), bus.GeneralChannel()}
	for _, group := range slices.Compact(groups) {
		if err := bus.ValidateGroupName(group); err != nil {
			return err
		}
		adapter.channels = append(adapter.channels, bus.GroupChannel(group))
	}
	return nil
}

// catalogue reads the directory's snapshot of the group channels and their readers. A directory
// that has none (trust.FileDirectory is the only one that can) gives an empty catalogue.
func (adapter *Adapter) catalogue() (trust.Catalogue, error) {
	snapshotter, ok := adapter.config.Directory.(interface {
		Catalogue() (trust.Catalogue, error)
	})
	if !ok {
		return trust.Catalogue{}, nil
	}
	return snapshotter.Catalogue()
}

// Close gives the identity up. The transport belongs to the caller.
func (adapter *Adapter) Close() error {
	if adapter.lock == nil {
		return nil
	}
	return errors.Join(adapter.handled.close(), adapter.lock.Release())
}

// Ready returns the error every operation fails with when another process holds the identity
// (§3), and nil otherwise.
func (adapter *Adapter) Ready() error { return adapter.lockErr }

func (adapter *Adapter) AgentID() string { return adapter.config.AgentID }

func (adapter *Adapter) AutoAccept() bool { return adapter.config.AutoAccept }

// DeliversAll says every message of the agent's channels reaches its inbox (--deliver=all).
func (adapter *Adapter) DeliversAll() bool { return adapter.config.Deliver == DeliverAll }

// Channels are the channels the adapter reads: the agent's DM channel, general and its groups.
func (adapter *Adapter) Channels() []bus.Channel { return slices.Clone(adapter.channels) }

// directoryRecords reads every record of the local directory. Only a directory that can be listed
// (trust.FileDirectory) supports it. Like FileDirectory.Records it returns the readable records
// together with an error naming the files it skipped.
func (adapter *Adapter) directoryRecords(ctx context.Context) ([]bus.AgentRecord, error) {
	lister, ok := adapter.config.Directory.(interface {
		Records(context.Context) ([]bus.AgentRecord, error)
	})
	if !ok {
		return nil, errors.New("this directory cannot list agents")
	}
	return lister.Records(ctx)
}

// Agents are the agents of the local directory that were not removed, sorted.
func (adapter *Adapter) Agents(ctx context.Context) ([]string, error) {
	if err := adapter.Ready(); err != nil {
		return nil, err
	}
	records, err := adapter.directoryRecords(ctx)
	if err != nil && len(records) == 0 {
		return nil, err
	}
	var agentIDs []string
	for _, record := range records {
		if !record.Removed() {
			agentIDs = append(agentIDs, record.AgentID)
		}
	}
	return agentIDs, nil
}

// RefreshDirectory re-reads the local directory and remembers the fingerprint of every revoked key
// in it, in revoked_keys. From then on messages signed by such a key are rejected, pinned or not.
// Revocations are only ever added: a directory that lost a record does not un-revoke anything. An
// unreadable file does not stop the others from being processed; the error names it.
func (adapter *Adapter) RefreshDirectory(ctx context.Context) error {
	if err := adapter.Ready(); err != nil {
		return err
	}
	records, skippedErr := adapter.directoryRecords(ctx)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for _, record := range records {
		for _, key := range record.Keys {
			if key.Revoked {
				if err := adapter.trust.Revoke(record.AgentID, key.Fingerprint); err != nil {
					return errors.Join(skippedErr, err)
				}
			}
		}
	}
	return skippedErr
}

func (adapter *Adapter) Fingerprint() string { return adapter.keyFP }

// Send sends a message to one agent's DM channel. The agent must be in the local directory.
func (adapter *Adapter) Send(ctx context.Context, to string, message Outgoing) (string, error) {
	if err := bus.ValidateAgentID(to); err != nil {
		return "", err
	}
	if err := adapter.Ready(); err != nil {
		return "", err
	}
	if err := adapter.checkRecipient(ctx, to); err != nil {
		return "", err
	}
	return adapter.post(ctx, bus.DMChannel(to), message)
}

// checkRecipient refuses an agent that has no directory record or was removed, as ErrBadTargets:
// the sender's mistake. A lookup that fails otherwise is returned as it is.
func (adapter *Adapter) checkRecipient(ctx context.Context, agentID string) error {
	record, err := adapter.config.Directory.Lookup(ctx, agentID)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: unknown agent %s: it has no entry in the directory", ErrBadTargets, agentID)
	}
	if err != nil {
		return err
	}
	if record.Removed() {
		return fmt.Errorf("%w: agent removed: %s", ErrBadTargets, agentID)
	}
	return nil
}

// Publish posts to `general` or a group channel the agent reads or the directory's catalogue
// lists. A post to any other group would be stored and never read, so it is refused.
func (adapter *Adapter) Publish(ctx context.Context, channelName string, message Outgoing) (string, error) {
	if channelName == "general" {
		return adapter.post(ctx, bus.GeneralChannel(), message)
	}
	if err := bus.ValidateGroupName(channelName); err != nil {
		return "", err
	}
	if !slices.Contains(adapter.channels, bus.GroupChannel(channelName)) {
		catalogue, err := adapter.catalogue()
		if err != nil {
			return "", err
		}
		if _, listed := catalogue[channelName]; !listed {
			return "", fmt.Errorf("unknown channel %s: it is not in the catalogue (busctl channel create) of this machine's directory", channelName)
		}
	}
	return adapter.post(ctx, bus.GroupChannel(channelName), message)
}

// NonReaders are the agents among the targets that the directory's catalogue does not list as
// readers of the group, for the sender to be warned: they would not see the message. general is
// read by everyone, and a group the catalogue does not know (the agent reads it through --groups)
// says nothing.
func (adapter *Adapter) NonReaders(channelName string, targets []string) []string {
	catalogue, err := adapter.catalogue()
	readers, listed := catalogue[channelName]
	if err != nil || !listed {
		return nil
	}
	var absent []string
	for _, target := range targets {
		if target != envelope.TargetAll && !slices.Contains(readers, target) {
			absent = append(absent, target)
		}
	}
	return absent
}

// parent is what a reply inherits from the message it answers.
type parent struct {
	id, thread, author string
	hops               int
}

// findParent looks for a message the agent has read or sent. The history lookup of §8 rule 3 for
// an unknown one comes with busd, so an unknown reply_to is refused.
func (adapter *Adapter) findParent(id string) (parent, bool) {
	if buffered := adapter.inbox.findByID(id); buffered != nil {
		return parent{id, buffered.Envelope.Thread, buffered.Envelope.From, buffered.Envelope.Hops}, true
	}
	if entry, ok := adapter.handled.messageByID(id); ok {
		return parent{id, entry.Thread, entry.From, entry.Hops}, true
	}
	return parent{}, false
}

// ErrBadTargets is the error of a message that is aimed at nobody or at an invalid target.
var ErrBadTargets = errors.New("invalid targets")

// targetsFor fills in the targets of an outgoing message (design.md §5): a DM targets its
// recipient; in a channel they are the ones given, else the mentions in the text, plus the author
// of the message being answered (always for an explicit reply_to, for an inferred parent only on
// closing and plain messages); a request or question with none goes to @all. Anything else aimed
// at nobody is refused.
func (adapter *Adapter) targetsFor(ctx context.Context, to bus.Channel, message Outgoing, replied *parent, explicitReply bool) ([]string, error) {
	if to.Kind == bus.DM {
		if message.Targets != nil && !slices.Equal(message.Targets, []string{to.Name}) {
			return nil, fmt.Errorf("%w: a DM targets its recipient %s", ErrBadTargets, to.Name)
		}
		return []string{to.Name}, nil
	}
	targets := message.Targets
	if targets == nil {
		targets = envelope.MentionedTargets(message.Body.Text)
	}
	// A parent inferred from recent reading (no explicit reply_to) only binds closing messages
	// (ack, answer, status, plain messages); a new question or request is a new topic for @all.
	bindsAuthor := explicitReply || !slices.Contains(envelope.ResponseBearingTypes, message.Type)
	if replied != nil && bindsAuthor && replied.author != adapter.config.AgentID && !slices.Contains(targets, envelope.TargetAll) && !slices.Contains(targets, replied.author) {
		targets = append(slices.Clone(targets), replied.author)
	}
	if len(targets) == 0 && slices.Contains(envelope.ResponseBearingTypes, message.Type) {
		targets = []string{envelope.TargetAll}
	}
	if err := (envelope.Envelope{Targets: targets}).CheckTargets(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadTargets, err)
	}
	for _, target := range targets {
		if target == envelope.TargetAll {
			continue
		}
		if err := adapter.checkRecipient(ctx, target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (adapter *Adapter) post(ctx context.Context, to bus.Channel, message Outgoing) (string, error) {
	if err := adapter.Ready(); err != nil {
		return "", err
	}
	if err := adapter.config.Transport.Unavailable(); err != nil {
		return "", err
	}
	if !slices.Contains(envelope.Types, message.Type) {
		return "", fmt.Errorf("unknown message type %q, want one of %v", message.Type, envelope.Types)
	}
	if secret := findSecret(message.Body); secret != "" {
		return "", fmt.Errorf("not sent: the body contains %s", secret)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	now := adapter.config.Now().UTC().Truncate(time.Second)

	var replyTo *string
	var inherited *parent
	if message.ReplyTo != "" {
		found, ok := adapter.findParent(message.ReplyTo)
		if !ok {
			return "", fmt.Errorf("not sent: reply_to %s is not a message this agent has read or sent", message.ReplyTo)
		}
		inherited = &found
	} else if adapter.recent.id != "" && now.Sub(adapter.recent.at) <= recentParentWindow {
		if found, ok := adapter.findParent(adapter.recent.id); ok {
			inherited = &found
		}
	}

	targets, err := adapter.targetsFor(ctx, to, message, inherited, message.ReplyTo != "")
	if err != nil {
		return "", err
	}

	id := envelope.NewID(now)
	hops, thread := 0, message.Thread
	if inherited != nil {
		if inherited.hops >= adapter.config.MaxHops {
			return "", fmt.Errorf("not sent: the conversation is %d replies deep, the limit is %d; wait for a human", inherited.hops, adapter.config.MaxHops)
		}
		hops = inherited.hops + 1
		replyTo = &inherited.id
		if thread == "" {
			thread = inherited.thread
		}
	}
	if thread == "" {
		thread = id
	}
	if !adapter.rate.allow(now) {
		return "", fmt.Errorf("not sent: rate limit of %d messages per %s reached", adapter.config.RateLimit, rateWindow)
	}

	payload, err := envelope.Sign(envelope.Envelope{
		Version: envelope.Version, ID: id, TS: now, From: adapter.config.AgentID, To: envelope.ToFor(to),
		Type: message.Type, Targets: targets, Thread: thread, ReplyTo: replyTo, Hops: hops, KeyFP: adapter.keyFP, Body: message.Body,
	}, adapter.key)
	if err != nil {
		return "", err
	}
	sequence, err := adapter.config.Transport.Publish(ctx, to, id, payload)
	if err != nil {
		return "", err
	}
	if message.Type == "ack" && replyTo != nil {
		if adapter.acked == nil {
			adapter.acked = map[string]bool{}
		}
		adapter.acked[*replyTo] = true
	}
	// Handled at send time with the sequence, so a message that comes back on general or a group
	// channel is not delivered to the agent again.
	err = adapter.handled.add(handledEntry{
		Kind: entryMessage, Destination: to.String(), Sequence: sequence, From: adapter.config.AgentID, ID: id,
		Digest: digestOf(payload), Thread: thread, Hops: hops, At: now,
	})
	if err != nil {
		return id, fmt.Errorf("message %s was sent but could not be recorded: %w", id, err)
	}
	return id, nil
}

func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Poll fetches each channel once and verifies what it got. It returns the errors of channels that
// could not be read after processing the rest.
func (adapter *Adapter) Poll(ctx context.Context) error {
	if err := adapter.Ready(); err != nil {
		return err
	}
	if err := adapter.config.Transport.Unavailable(); err != nil {
		return err
	}
	adapter.pollMu.Lock()
	defer adapter.pollMu.Unlock()
	var errs []error
	for _, channel := range adapter.channels {
		adapter.mu.Lock()
		batch := adapter.inbox.fetchBatch()
		adapter.mu.Unlock()
		if batch == 0 {
			break // full: resume when the agent has acked something
		}
		deliveries, err := adapter.config.Transport.Fetch(ctx, channel, batch)
		errs = append(errs, err)
		adapter.mu.Lock()
		for _, delivery := range deliveries {
			errs = append(errs, adapter.receive(ctx, channel, delivery))
		}
		adapter.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Run reads until the context ends and keeps buffered messages alive at the broker. It does not
// report that the bus is unavailable: the connection tells of an outage and its end itself, and it
// reads on when the bus is back.
func (adapter *Adapter) Run(ctx context.Context, report func(error)) error {
	if err := adapter.Ready(); err != nil {
		return err
	}
	lastKeepAlive := time.Now()
	for ctx.Err() == nil {
		before := adapter.buffered()
		if err := adapter.Poll(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, bus.ErrUnavailable) {
			report(err)
		}
		if time.Since(lastKeepAlive) >= keepAliveInterval {
			adapter.mu.Lock()
			err := adapter.inbox.keepAlive(ctx)
			adapter.mu.Unlock()
			if err != nil && ctx.Err() == nil && !errors.Is(err, bus.ErrUnavailable) {
				report(err)
			}
			lastKeepAlive = time.Now()
		}
		if adapter.buffered() == before {
			select {
			case <-ctx.Done():
			case <-time.After(idlePause):
			}
		}
	}
	return nil
}

// noteUnknownSender remembers a message that waits because its sender has no directory record.
func (adapter *Adapter) noteUnknownSender(sender, destination string, sequence uint64) {
	if adapter.unknownSenders == nil {
		adapter.unknownSenders = map[string]map[string]bool{}
	}
	if adapter.unknownSenders[sender] == nil {
		adapter.unknownSenders[sender] = map[string]bool{}
	}
	adapter.unknownSenders[sender][fmt.Sprintf("%s/%d", destination, sequence)] = true
}

// UnknownSenders returns the senders whose messages wait for a directory record, and how many
// messages that is.
func (adapter *Adapter) UnknownSenders() (senders []string, messages int) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for sender, waiting := range adapter.unknownSenders {
		senders = append(senders, sender)
		messages += len(waiting)
	}
	slices.Sort(senders)
	return senders, messages
}

func (adapter *Adapter) buffered() int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return len(adapter.inbox.messages)
}

// receive runs the state machine of design.md §4a for one delivery. The caller holds mu. A
// delivery it cannot decide about is left unacknowledged: the broker redelivers it after AckWait.
func (adapter *Adapter) receive(ctx context.Context, channel bus.Channel, delivery bus.Delivery) error {
	destination := channel.String()
	if adapter.handled.sequenceHandled(destination, delivery.Sequence) {
		return delivery.Ack(ctx)
	}

	signed, err := envelope.Parse(delivery.Payload)
	if err != nil {
		return adapter.reject(ctx, channel, delivery, err)
	}
	env := signed.Envelope
	if err := bus.ValidateAgentID(env.From); err != nil {
		return adapter.reject(ctx, channel, delivery, err)
	}
	if err := env.CheckTo(channel); err != nil {
		return adapter.reject(ctx, channel, delivery, err)
	}
	resolution, err := adapter.trust.Resolve(ctx, adapter.config.Directory, env.From, env.KeyFP, adapter.config.Now())
	if errors.Is(err, fs.ErrNotExist) {
		adapter.noteUnknownSender(env.From, destination, delivery.Sequence)
	} else {
		delete(adapter.unknownSenders, env.From)
	}
	if errors.Is(err, trust.ErrUnavailable) {
		return adapter.alert("%s sequence %d from %s left unread for now: %v", destination, delivery.Sequence, env.From, err)
	}
	if err != nil {
		return adapter.reject(ctx, channel, delivery, err)
	}
	if !signed.Verify(resolution.Key) {
		return adapter.reject(ctx, channel, delivery, fmt.Errorf("signature of %s does not verify", env.From))
	}
	if err := env.CheckNotFuture(adapter.config.Now()); err != nil {
		return adapter.reject(ctx, channel, delivery, err)
	}
	if resolution.NewPin != nil {
		if err := adapter.trust.Add(*resolution.NewPin); err != nil {
			return err
		}
	}

	digest := digestOf(delivery.Payload)
	if entry, handled := adapter.handled.message(destination, env.From, env.ID); handled {
		if entry.Digest != digest {
			return adapter.reject(ctx, channel, delivery, fmt.Errorf("ID %s of %s was handled with another payload", env.ID, env.From))
		}
		entry.Sequence = delivery.Sequence
		if err := adapter.handled.add(entry); err != nil {
			return err
		}
		return delivery.Ack(ctx)
	}
	if buffered := adapter.inbox.find(channel, env.From, env.ID); buffered != nil {
		if buffered.Digest != digest {
			return adapter.reject(ctx, channel, delivery, fmt.Errorf("ID %s of %s is buffered with another payload", env.ID, env.From))
		}
		return delivery.InProgress(ctx)
	}
	message := &Message{
		Envelope: env, Channel: channel, Sequence: delivery.Sequence, FirstContact: resolution.NewPin != nil,
		TargetsMe: adapter.targetsMe(env), Digest: digest, ArrivedAt: adapter.config.Now(), delivery: delivery, size: len(delivery.Payload),
	}
	if !message.TargetsMe && adapter.config.Deliver == DeliverTargeted {
		// Chatter costs the agent no context: handled and acked without ever reaching the inbox.
		if err := adapter.handled.add(adapter.handledEntry(message)); err != nil {
			return err
		}
		return delivery.Ack(ctx)
	}
	if !message.TargetsMe {
		if oldest := adapter.inbox.contextFull(); oldest != nil {
			if err := adapter.dropContext(ctx, oldest); err != nil {
				return err
			}
		}
	}
	adapter.inbox.add(message)
	return nil
}

// dropContext handles and acks a context message the agent never read, to make room for newer
// context. The caller holds mu.
func (adapter *Adapter) dropContext(ctx context.Context, message *Message) error {
	if err := adapter.handled.add(adapter.handledEntry(message)); err != nil {
		return err
	}
	if err := message.delivery.Ack(ctx); err != nil {
		return err
	}
	adapter.inbox.remove(message)
	adapter.contextDropped++
	return nil
}

// TakeContextDropped returns how many context messages were dropped unread since the last call,
// for the agent to be told.
func (adapter *Adapter) TakeContextDropped() int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	dropped := adapter.contextDropped
	adapter.contextDropped = 0
	return dropped
}

// targetsMe is the rule of §5: this agent's ID or "@all" in the targets. The sender put the
// recipient of a DM and the author of a replied-to message there.
func (adapter *Adapter) targetsMe(env envelope.Envelope) bool {
	return slices.Contains(env.Targets, adapter.config.AgentID) || slices.Contains(env.Targets, envelope.TargetAll)
}

func (adapter *Adapter) handledEntry(message *Message) handledEntry {
	env := message.Envelope
	return handledEntry{
		Kind: entryMessage, Destination: message.Channel.String(), Sequence: message.Sequence, From: env.From, ID: env.ID,
		Digest: message.Digest, Thread: env.Thread, Hops: env.Hops, At: adapter.config.Now().UTC(),
	}
}

// reject records a delivery as handled by its sequence, alerts and acks it: it will never be
// shown to the agent (§6 terminal rejection).
func (adapter *Adapter) reject(ctx context.Context, channel bus.Channel, delivery bus.Delivery, reason error) error {
	entry := handledEntry{Kind: entryRejected, Destination: channel.String(), Sequence: delivery.Sequence, At: adapter.config.Now().UTC()}
	if err := adapter.handled.add(entry); err != nil {
		return err
	}
	alertErr := adapter.alert("rejected %s sequence %d: %v", channel, delivery.Sequence, reason)
	return errors.Join(alertErr, delivery.Ack(ctx))
}

// alert appends a line to alerts.log; busctl alerts will read it.
func (adapter *Adapter) alert(format string, args ...any) error {
	line := fmt.Sprintf("%s %s\n", adapter.config.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	file, err := os.OpenFile(adapter.alertsPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, files.SecretMode)
	if err != nil {
		return err
	}
	_, err = file.WriteString(line)
	return errors.Join(err, file.Close())
}

// Inbox lists the verified messages the agent has not acked: those that target it first, each
// group oldest first. Reading them makes the newest targeted one (or else the newest) the parent
// of the agent's next reply (§8 rule 3).
func (adapter *Adapter) Inbox(query InboxQuery) ([]Message, error) {
	if err := adapter.Ready(); err != nil {
		return nil, err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	var listed []Message
	for _, message := range adapter.inbox.messages {
		if len(query.Types) > 0 && !slices.Contains(query.Types, message.Envelope.Type) {
			continue
		}
		if query.ID != "" && message.Envelope.ID != query.ID {
			continue
		}
		if query.Channel != nil && message.Channel != *query.Channel {
			continue
		}
		if query.NewOnly && message.returned {
			continue
		}
		listed = append(listed, *message)
		listed[len(listed)-1].Acked = adapter.acked[message.Envelope.ID]
	}
	slices.SortStableFunc(listed, func(a, b Message) int {
		switch {
		case a.TargetsMe == b.TargetsMe:
			return 0
		case a.TargetsMe:
			return -1
		}
		return 1
	})
	if query.Limit > 0 && len(listed) > query.Limit {
		listed = listed[:query.Limit]
	}
	if query.Peek {
		return listed, nil
	}
	for _, message := range listed {
		adapter.inbox.findByID(message.Envelope.ID).returned = true
	}
	if len(listed) > 0 {
		// Targeted messages come first, so the one before the first other is the newest of them.
		parent := len(listed) - 1
		if lastTargeted := slices.IndexFunc(listed, func(m Message) bool { return !m.TargetsMe }) - 1; lastTargeted >= 0 {
			parent = lastTargeted
		}
		adapter.recent.id, adapter.recent.at = listed[parent].Envelope.ID, adapter.config.Now()
	}
	return listed, nil
}

// Ack acks the broker first and only then records the message as handled and drops it from the
// inbox, so an ack the broker did not confirm leaves the message unread. A crash between the two
// steps loses nothing the agent did not ack: the broker will not redeliver it, and the handled
// entry, which only skips redeliveries, is missing. It goes on after an error and reports every one.
func (adapter *Adapter) Ack(ctx context.Context, ids []string) error {
	if err := adapter.Ready(); err != nil {
		return err
	}
	// Before anything changes: an ack that cannot reach the broker leaves the message unread.
	if err := adapter.config.Transport.Unavailable(); err != nil {
		return err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	var errs []error
	for _, id := range ids {
		message := adapter.inbox.findByID(id)
		if message == nil {
			errs = append(errs, fmt.Errorf("no unread message %s", id))
			continue
		}
		if err := message.delivery.Ack(ctx); err != nil {
			errs = append(errs, fmt.Errorf("message %s stays unread, the broker did not confirm the ack: %w", id, err))
			continue
		}
		adapter.recent.id, adapter.recent.at = message.Envelope.ID, adapter.config.Now()
		adapter.inbox.remove(message)
		if err := adapter.handled.add(adapter.handledEntry(message)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
