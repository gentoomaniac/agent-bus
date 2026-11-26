package busd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

const (
	// channelRequestInterval and channelRequestBurst bound the channel requests busd answers in
	// all, callerChannelInterval and callerChannelBurst each agent's: a person or a model asks a
	// few times a minute, and every change costs a confirmed reload or a consumer.
	channelRequestInterval = 100 * time.Millisecond
	channelRequestBurst    = 20
	callerChannelInterval  = 2 * time.Second
	callerChannelBurst     = 5
	// creationWindow is the "day" of the creation limit: a rolling window, so the next creation
	// is allowed a day after the oldest one that counts.
	creationWindow = 24 * time.Hour
	// channelRequestTimeout bounds one request: a new channel includes a confirmed reload of the
	// grants.
	channelRequestTimeout = time.Minute
)

// ChannelAdmin is what the channel requests read and change on the broker. *broker.Admin
// implements it.
type ChannelAdmin interface {
	trust.CatalogueSource
	Agents(ctx context.Context) ([]bus.AgentRecord, error)
	AgentAccounts(ctx context.Context) ([]string, error)
	CreateGroupChannel(ctx context.Context, record bus.TopicRecord) (created bool, err error)
	UpdateGroupChannel(ctx context.Context, name string, change func(record *bus.TopicRecord)) error
	CreateReadCursor(ctx context.Context, agentID string, channel bus.Channel, afterSequence uint64) error
	DeleteReadCursor(ctx context.Context, agentID string, channel bus.Channel) error
	LastSequence(ctx context.Context) (uint64, error)
	Retention(ctx context.Context) (bus.Retention, error)
	GroupReaders(ctx context.Context) (map[string][]string, error)
}

// ChannelsConfig is what Channels are made of.
type ChannelsConfig struct {
	Admin ChannelAdmin
	CA    *tlsinit.CA // signs the replies
	// Lookup is the record of an agent in the published directory (Directory.Record): a request's
	// signature is checked against it, so only agents whose enrollment is complete take part. A
	// change checks the registry again under the lock (eligible).
	Lookup func(agentID string) (bus.AgentRecord, bool)
	// BusOwners are the agent IDs that act as the bus owner: they manage every channel, own the
	// channels agent-busctl made, and are exempt from the creation limit.
	BusOwners []string
	// CreateLimit is how many channels an agent may create in a day; 0 sets no limit.
	CreateLimit int
	// LockMutations takes the lock shared with approvals and agent-busctl --direct while a request
	// changes the catalogue or the cursors. Nil: no lock, for tests.
	LockMutations func() (unlock func(), err error)
	// DirectoryChanged is told after a change, so the published directory, and with it every
	// agent's set of channels, follows at once. Nil: nobody is told.
	DirectoryChanged func()
	Now              func() time.Time // default time.Now
}

// Channels answer the channel requests of agents (docs/design/agent-channels.md).
type Channels struct {
	config ChannelsConfig
	limits *callerLimits
}

func NewChannels(config ChannelsConfig) *Channels {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Channels{config: config, limits: newCallerLimits(channelRequestInterval, channelRequestBurst, callerChannelInterval, callerChannelBurst)}
}

// HandleRequest answers a channel request of caller, the agent whose subject it came on. The
// request must be signed with the caller's key as the published directory has it. The reply is
// signed and bound to the request; past a rate limit busd answers unsigned, without spending a
// signature.
func (channels *Channels) HandleRequest(caller string, message []byte) []byte {
	if !channels.limits.allow(caller) {
		return directory.Unsigned("too many channel requests, try again in a few seconds")
	}
	reply := channelapi.Reply{RequestHash: directory.RequestHash(message)}
	err := fmt.Errorf("%s is not in the directory", caller)
	if record, known := channels.config.Lookup(caller); known {
		var request channelapi.Request
		if request, err = channelapi.Open(message, record); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), channelRequestTimeout)
			err = channels.answer(ctx, request, record, &reply)
			cancel()
		}
	}
	if err != nil {
		reply = channelapi.Reply{RequestHash: reply.RequestHash, Error: err.Error()}
	}
	signed, err := channelapi.SignReply(channels.config.CA, reply)
	if err != nil {
		log.Error().Msgf("busd: signing a channel reply: %v", err)
		return directory.Unsigned("busd cannot sign its answer")
	}
	return signed
}

// answer carries out the request, which was signed with the key of signer, the caller's record in
// the published directory.
func (channels *Channels) answer(ctx context.Context, request channelapi.Request, signer bus.AgentRecord, reply *channelapi.Reply) error {
	if request.Action == channelapi.List {
		var err error
		reply.Channels, reply.Next, err = channels.list(ctx, request.AgentID, request.After)
		return err
	}
	if err := bus.ValidateGroupName(request.Channel); err != nil {
		return err
	}
	if request.Action == channelapi.Members {
		var err error
		reply.Channel, reply.Next, err = channels.describe(ctx, request.Channel, request.After)
		return err
	}
	if channels.config.LockMutations != nil {
		unlock, err := channels.config.LockMutations()
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := channels.eligible(ctx, request.AgentID, signer.PublicKey); err != nil {
		return err
	}
	var err error
	switch request.Action {
	case channelapi.Create:
		reply.Limits, err = channels.create(ctx, request)
	case channelapi.Join:
		err = channels.addMember(ctx, request.Channel, request.AgentID)
	case channelapi.Leave:
		err = channels.removeMember(ctx, request.Channel, request.AgentID)
	case channelapi.Invite, channelapi.RemoveMember, channelapi.Update:
		err = channels.manage(ctx, request)
	}
	if err != nil {
		return err
	}
	if channels.config.DirectoryChanged != nil {
		channels.config.DirectoryChanged()
	}
	reply.Channel, reply.Next, err = channels.describe(ctx, request.Channel, "")
	return err
}

// manage carries out what only the channel owner or the bus owner may do.
func (channels *Channels) manage(ctx context.Context, request channelapi.Request) error {
	record, err := channels.find(ctx, request.Channel)
	if err != nil {
		return err
	}
	if record.Owner != request.AgentID && !channels.isBusOwner(request.AgentID) {
		return fmt.Errorf("only the owner of channel %s (%s) or the bus owner may %s", request.Channel, ownerName(record.Owner), strings.ReplaceAll(request.Action, "_", " "))
	}
	switch request.Action {
	case channelapi.Invite:
		if err := channels.eligible(ctx, request.Agent, ""); err != nil {
			return err
		}
		return channels.addMember(ctx, request.Channel, request.Agent)
	case channelapi.RemoveMember:
		return channels.removeMember(ctx, request.Channel, request.Agent)
	default:
		if err := checkDescription(request.Description); err != nil {
			return err
		}
		return channels.config.Admin.UpdateGroupChannel(ctx, request.Channel, func(record *bus.TopicRecord) { record.Description = request.Description })
	}
}

// eligible checks, under the mutation lock, that the agent is still on the bus: a registry record
// that is not removed, an account, and, with a key given, that key still its current one. The
// published directory a request was checked against may be seconds old, and a removal by
// agent-busctl may have run while the request waited for the lock: a cursor made now would outlive
// the agent.
func (channels *Channels) eligible(ctx context.Context, agentID, publicKey string) error {
	records, err := channels.config.Admin.Agents(ctx)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(records, func(record bus.AgentRecord) bool { return record.AgentID == agentID })
	if index < 0 || records[index].Removed() {
		return fmt.Errorf("%q is not an agent of the bus", agentID)
	}
	if publicKey != "" && records[index].PublicKey != publicKey {
		return fmt.Errorf("the key of %s changed since the request was signed: ask again", agentID)
	}
	accounts, err := channels.config.Admin.AgentAccounts(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(accounts, agentID) {
		return fmt.Errorf("%q has no account on the broker", agentID)
	}
	return nil
}

func ownerName(owner string) string { return cmp.Or(owner, "the bus owner") }

func (channels *Channels) isBusOwner(agentID string) bool {
	return slices.Contains(channels.config.BusOwners, agentID)
}

func checkDescription(description string) error {
	if strings.TrimSpace(description) == "" {
		return errors.New("a channel needs a description: what it is for")
	}
	if len(description) > channelapi.MaxDescriptionBytes {
		return fmt.Errorf("the description is longer than %d bytes", channelapi.MaxDescriptionBytes)
	}
	return nil
}

// create makes the channel with the caller as its owner and first member, within the caller's
// creation limit, and returns the limits the channel lives under.
//
// The entry is written first, marked pending, with the owner's start in the log: that takes the
// name and counts against the limit at once. Then come the grants and the owner's read cursor,
// and then busd clears the mark. Every step can be repeated, so the owner's next create finishes
// a pending channel as the first attempt meant it; others are refused its name meanwhile. A
// create of a complete channel the caller owns changes nothing: a late retry must not bring back
// a member that left.
func (channels *Channels) create(ctx context.Context, request channelapi.Request) (*channelapi.Limits, error) {
	if err := checkDescription(request.Description); err != nil {
		return nil, err
	}
	retention, err := channels.config.Admin.Retention(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := channels.config.Admin.GroupChannels(ctx)
	if err != nil {
		return nil, err
	}
	now := channels.config.Now().UTC()
	left, exempt := channels.creationsLeft(groups, request.AgentID, now)
	index := slices.IndexFunc(groups, func(group bus.TopicRecord) bool { return group.Name == request.Channel })
	var record bus.TopicRecord
	switch {
	case index >= 0 && groups[index].Owner != request.AgentID:
		return nil, fmt.Errorf("channel %s exists: join it, or pick another name", request.Channel)
	case index >= 0:
		record = groups[index]
	case !exempt && left <= 0:
		return nil, fmt.Errorf("limit of %d new channels per day reached; next at %s",
			channels.config.CreateLimit, channels.nextCreation(groups, request.AgentID, now).Format("2006-01-02T15:04Z"))
	default:
		last, err := channels.config.Admin.LastSequence(ctx)
		if err != nil {
			return nil, err
		}
		record = bus.TopicRecord{
			Name: request.Channel, Description: request.Description, Owner: request.AgentID, CreatedAt: now, UpdatedAt: now,
			Pending: true, OwnerStart: last,
		}
		left--
	}
	if record.Pending {
		if err := channels.finish(ctx, record); err != nil {
			return nil, err
		}
	}
	limits := &channelapi.Limits{}
	if retention.MaxAge > 0 {
		limits.MessageRetention = ptr(days(retention.MaxAge))
	}
	if retention.MaxBytes > 0 {
		limits.StreamMaxBytes = ptr(retention.MaxBytes)
	}
	if !exempt {
		limits.ChannelsLeftToday, limits.CreationLimitPerDay = ptr(left), ptr(channels.config.CreateLimit)
	}
	return limits, nil
}

// finish makes a pending channel complete: its entry (kept if it exists) with the grants, the
// owner's read cursor at the entry's start (kept if it exists), and then the cleared mark.
func (channels *Channels) finish(ctx context.Context, record bus.TopicRecord) error {
	admin := channels.config.Admin
	if _, err := admin.CreateGroupChannel(ctx, record); err != nil {
		return err
	}
	if err := admin.CreateReadCursor(ctx, record.Owner, bus.GroupChannel(record.Name), record.OwnerStart); err != nil {
		return err
	}
	return admin.UpdateGroupChannel(ctx, record.Name, func(entry *bus.TopicRecord) { entry.Pending = false })
}

// creationsLeft is how many channels the agent may still create in the day up to now, and whether
// it is exempt from the limit: the bus owner is, and everyone when there is no limit.
func (channels *Channels) creationsLeft(groups []bus.TopicRecord, agentID string, now time.Time) (left int, exempt bool) {
	if channels.config.CreateLimit <= 0 || channels.isBusOwner(agentID) {
		return 0, true
	}
	return channels.config.CreateLimit - len(recentCreations(groups, agentID, now)), false
}

// nextCreation is when the oldest creation that counts leaves the window.
func (channels *Channels) nextCreation(groups []bus.TopicRecord, agentID string, now time.Time) time.Time {
	recent := recentCreations(groups, agentID, now)
	excess := len(recent) - channels.config.CreateLimit // creations that must leave before one more fits
	return recent[excess].Add(creationWindow)
}

// recentCreations are the times the agent created the channels it owns in the window up to now,
// oldest first. The catalogue is the record of them, so a deleted channel no longer counts.
func recentCreations(groups []bus.TopicRecord, agentID string, now time.Time) []time.Time {
	var times []time.Time
	for _, group := range groups {
		if group.Owner == agentID && now.Sub(group.CreatedAt) < creationWindow {
			times = append(times, group.CreatedAt)
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	return times
}

func (channels *Channels) find(ctx context.Context, name string) (bus.TopicRecord, error) {
	groups, err := channels.config.Admin.GroupChannels(ctx)
	if err != nil {
		return bus.TopicRecord{}, err
	}
	index := slices.IndexFunc(groups, func(group bus.TopicRecord) bool { return group.Name == name })
	if index < 0 || groups[index].Pending {
		return bus.TopicRecord{}, fmt.Errorf("there is no channel %s", name)
	}
	return groups[index], nil
}

// addMember gives the agent a read cursor on the channel from the current end of the log on, as
// `agent-busctl channel join` does; its grants for the cursor came with the channel. A member stays
// where it is.
func (channels *Channels) addMember(ctx context.Context, name, agentID string) error {
	if _, err := channels.find(ctx, name); err != nil {
		return err
	}
	last, err := channels.config.Admin.LastSequence(ctx)
	if err != nil {
		return err
	}
	return channels.config.Admin.CreateReadCursor(ctx, agentID, bus.GroupChannel(name), last)
}

func (channels *Channels) removeMember(ctx context.Context, name, agentID string) error {
	if _, err := channels.find(ctx, name); err != nil {
		return err
	}
	return channels.config.Admin.DeleteReadCursor(ctx, agentID, bus.GroupChannel(name))
}

// describe is the channel with the page of its members that follows after.
func (channels *Channels) describe(ctx context.Context, name, after string) (channel *channelapi.Channel, next string, err error) {
	record, err := channels.find(ctx, name)
	if err != nil {
		return nil, "", err
	}
	members, err := channels.config.Admin.ReadCursorHolders(ctx, bus.GroupChannel(name))
	if err != nil {
		return nil, "", err
	}
	slices.Sort(members)
	members, next = pageOf(members, func(id string) string { return id }, after, channelapi.MaxListedMembers)
	return &channelapi.Channel{Name: record.Name, Description: record.Description, Owner: record.Owner, Members: members}, next, nil
}

// list is the page of channels by name that follows after, with whether the caller reads each. The
// consumers are listed once for all channels.
func (channels *Channels) list(ctx context.Context, caller, after string) (list []channelapi.Channel, next string, err error) {
	groups, err := channels.config.Admin.GroupChannels(ctx)
	if err != nil {
		return nil, "", err
	}
	readers, err := channels.config.Admin.GroupReaders(ctx)
	if err != nil {
		return nil, "", err
	}
	groups = slices.DeleteFunc(groups, func(group bus.TopicRecord) bool { return group.Pending })
	slices.SortFunc(groups, func(a, b bus.TopicRecord) int { return strings.Compare(a.Name, b.Name) })
	list = []channelapi.Channel{}
	for _, group := range groups {
		list = append(list, channelapi.Channel{
			Name: group.Name, Description: group.Description, Owner: group.Owner, Joined: slices.Contains(readers[group.Name], caller),
		})
	}
	list, next = pageOf(list, func(channel channelapi.Channel) string { return channel.Name }, after, channelapi.MaxListedChannels)
	return list, next, nil
}

// pageOf takes, in order, the items whose key comes after after (all of them for ""), while they
// are at most limit and their JSON at most channelapi.MaxPageBytes; at least one is taken. items
// are sorted by key. next is the key of the last one taken when more follow, else "".
func pageOf[T any](items []T, key func(T) string, after string, limit int) (page []T, next string) {
	page = []T{}
	size := 0
	for _, item := range items {
		if after != "" && key(item) <= after {
			continue
		}
		data, _ := json.Marshal(item) // channels and IDs always marshal
		if len(page) > 0 && (len(page) == limit || size+len(data) > channelapi.MaxPageBytes) {
			return page, key(page[len(page)-1])
		}
		size += len(data) + 1 // and a comma
		page = append(page, item)
	}
	return page, ""
}

// days writes a duration as whole days ("90d") when it is, else as Go does.
func days(duration time.Duration) string {
	const day = 24 * time.Hour
	if duration%day == 0 {
		return fmt.Sprintf("%dd", duration/day)
	}
	return duration.String()
}

func ptr[T any](value T) *T { return &value }
