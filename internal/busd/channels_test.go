package busd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// What the channel requests change on the broker, beyond what approval needs.
// CreateGroupChannel writes the entry and then regenerates the grants, which createErr fails, as
// the broker's does: the entry stays.
func (fake *fakeRegistry) CreateGroupChannel(_ context.Context, record bus.TopicRecord) (bool, error) {
	created := !slices.ContainsFunc(fake.groups, func(group bus.TopicRecord) bool { return group.Name == record.Name })
	if created {
		fake.groups = append(fake.groups, record)
	}
	return created, fake.createErr
}
func (fake *fakeRegistry) DescribeGroupChannel(_ context.Context, name, description string) error {
	for i := range fake.groups {
		if fake.groups[i].Name == name {
			fake.groups[i].Description = description
			return nil
		}
	}
	return errors.New("no such channel")
}
func (fake *fakeRegistry) DeleteReadCursor(_ context.Context, agentID string, channel bus.Channel) error {
	prefix := agentID + " " + channel.String() + " after "
	fake.cursors = slices.DeleteFunc(fake.cursors, func(cursor string) bool { return strings.HasPrefix(cursor, prefix) })
	return nil
}
func (fake *fakeRegistry) GroupReaders(ctx context.Context) (map[string][]string, error) {
	readers := map[string][]string{}
	for _, group := range fake.groups {
		holders, _ := fake.ReadCursorHolders(ctx, bus.GroupChannel(group.Name))
		readers[group.Name] = holders
	}
	return readers, nil
}
func (*fakeRegistry) Retention(context.Context) (bus.Retention, error) {
	return bus.Retention{MaxAge: 90 * 24 * time.Hour, MaxBytes: 1 << 30}, nil
}

const (
	owner   = "owner@x"
	member  = "member@x"
	visitor = "visitor@x"
	boss    = "monitor@boss" // the bus owner
)

// channelFixture is busd's channel requests over the fake registry, with agents enrolled and in
// the published directory.
type channelFixture struct {
	*fixture
	channels *Channels
	keys     map[string]ed25519.PrivateKey
	changes  int // how often the directory was told of a change
}

func newChannelFixture(t *testing.T) *channelFixture {
	f := &channelFixture{fixture: newFixture(t), keys: map[string]ed25519.PrivateKey{}}
	for _, agentID := range []string{owner, member, visitor, boss} {
		f.keys[agentID] = f.newKey()
		f.enroll(agentID, f.keys[agentID])
	}
	published := f.directory()
	if _, err := published.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(f.dir, "ca.pem"), filepath.Join(f.dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	f.channels = NewChannels(ChannelsConfig{
		Admin: f.registry, CA: ca, Lookup: published.Record, BusOwners: []string{boss}, CreateLimit: 2,
		DirectoryChanged: func() { f.changes++ }, Now: func() time.Time { return f.now },
	})
	f.channels.limits.all = rate.NewLimiter(rate.Inf, 1) // limits are tested on their own
	f.channels.limits.callerEvery = time.Nanosecond
	return f
}

// ask sends request as its agent, signed with that agent's key, and returns busd's verified reply.
func (f *channelFixture) ask(request channelapi.Request) (channelapi.Reply, error) {
	f.t.Helper()
	message, err := channelapi.Sign(request, f.keys[request.AgentID])
	if err != nil {
		f.t.Fatal(err)
	}
	return channelapi.VerifyReply(mustRead(f.t, filepath.Join(f.dir, "ca.pem")), f.channels.HandleRequest(request.AgentID, message), message)
}

func (f *channelFixture) must(request channelapi.Request) channelapi.Reply {
	f.t.Helper()
	reply, err := f.ask(request)
	if err != nil {
		f.t.Fatalf("%s %s: %v", request.AgentID, request.Action, err)
	}
	return reply
}

func (f *channelFixture) create(agentID, name string) (channelapi.Reply, error) {
	return f.ask(channelapi.Request{AgentID: agentID, Action: channelapi.Create, Channel: name, Description: "about " + name})
}

func TestAnAgentCreatesAChannelItOwnsAndReads(t *testing.T) {
	f := newChannelFixture(t)
	reply, err := f.create(owner, "agent-bus")
	if err != nil {
		t.Fatal(err)
	}
	want := channelapi.Channel{Name: "agent-bus", Description: "about agent-bus", Owner: owner, Members: []string{owner}}
	if reply.Channel == nil || !slices.Equal(reply.Channel.Members, want.Members) || reply.Channel.Owner != owner || reply.Channel.Description != want.Description {
		t.Errorf("channel = %+v, want %+v", reply.Channel, want)
	}
	limits := reply.Limits
	if limits == nil || *limits.MessageRetention != "90d" || *limits.StreamMaxBytes != 1<<30 || *limits.ChannelsLeftToday != 1 ||
		*limits.CreationLimitPerDay != 2 || limits.IdleDeleteAfter != nil || limits.IdleWarningAfter != nil {
		t.Errorf("limits = %+v", limits)
	}
	if f.changes != 1 {
		t.Errorf("the directory was told of %d changes, want 1", f.changes)
	}
	if _, err := f.create(member, "agent-bus"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("a second channel of the same name: %v", err)
	}
	for _, refused := range []channelapi.Request{
		{AgentID: owner, Action: channelapi.Create, Channel: "no-description"},
		{AgentID: owner, Action: channelapi.Create, Channel: "Not A Name", Description: "x"},
		{AgentID: owner, Action: channelapi.Create, Channel: "general", Description: "x"},
	} {
		if _, err := f.ask(refused); err == nil {
			t.Errorf("%+v was accepted", refused)
		}
	}
}

func TestJoinLeaveAndListArePublic(t *testing.T) {
	f := newChannelFixture(t)
	f.registry.last = 41
	if _, err := f.create(owner, "agent-bus"); err != nil {
		t.Fatal(err)
	}
	if reply := f.must(channelapi.Request{AgentID: visitor, Action: channelapi.Join, Channel: "agent-bus"}); !slices.Equal(reply.Channel.Members, []string{owner, visitor}) {
		t.Errorf("after the join the members are %v", reply.Channel.Members)
	}
	if !slices.Contains(f.registry.cursors, visitor+" channel agent-bus after 41") {
		t.Errorf("the cursor does not start at the end of the log: %v", f.registry.cursors)
	}
	list := f.must(channelapi.Request{AgentID: member, Action: channelapi.List}).Channels
	if len(list) != 1 || list[0].Name != "agent-bus" || list[0].Owner != owner || list[0].Joined || list[0].Members != nil {
		t.Errorf("list for a non-member = %+v", list)
	}
	if list := f.must(channelapi.Request{AgentID: visitor, Action: channelapi.List}).Channels; !list[0].Joined {
		t.Errorf("list for a member = %+v", list)
	}
	if members := f.must(channelapi.Request{AgentID: member, Action: channelapi.Members, Channel: "agent-bus"}).Channel; !slices.Equal(members.Members, []string{owner, visitor}) {
		t.Errorf("members = %+v", members)
	}
	if reply := f.must(channelapi.Request{AgentID: visitor, Action: channelapi.Leave, Channel: "agent-bus"}); !slices.Equal(reply.Channel.Members, []string{owner}) {
		t.Errorf("after the leave the members are %v", reply.Channel.Members)
	}
	if _, err := f.ask(channelapi.Request{AgentID: visitor, Action: channelapi.Join, Channel: "nowhere"}); err == nil || !strings.Contains(err.Error(), "no channel nowhere") {
		t.Errorf("joining a channel that does not exist: %v", err)
	}
}

func TestOnlyTheChannelOwnerOrTheBusOwnerManagesAChannel(t *testing.T) {
	f := newChannelFixture(t)
	if _, err := f.create(owner, "agent-bus"); err != nil {
		t.Fatal(err)
	}
	f.registry.groups = append(f.registry.groups, bus.TopicRecord{Name: "lib-x", Description: "made by agent-busctl"})

	invite := channelapi.Request{AgentID: owner, Action: channelapi.Invite, Channel: "agent-bus", Agent: member}
	if reply := f.must(invite); !slices.Equal(reply.Channel.Members, []string{member, owner}) {
		t.Errorf("after the invite the members are %v", reply.Channel.Members)
	}
	for _, refused := range []channelapi.Request{
		{AgentID: visitor, Action: channelapi.Invite, Channel: "agent-bus", Agent: visitor},
		{AgentID: visitor, Action: channelapi.RemoveMember, Channel: "agent-bus", Agent: member},
		{AgentID: visitor, Action: channelapi.Update, Channel: "agent-bus", Description: "mine now"},
		{AgentID: owner, Action: channelapi.Invite, Channel: "lib-x", Agent: member}, // the bus owner's channel
	} {
		if _, err := f.ask(refused); err == nil || !strings.Contains(err.Error(), "only the owner") {
			t.Errorf("%s %s %s: %v", refused.AgentID, refused.Action, refused.Channel, err)
		}
	}
	if _, err := f.ask(channelapi.Request{AgentID: owner, Action: channelapi.Invite, Channel: "agent-bus", Agent: "stranger@x"}); err == nil || !strings.Contains(err.Error(), "not an agent") {
		t.Errorf("an invite of an agent the directory does not know: %v", err)
	}

	f.must(channelapi.Request{AgentID: boss, Action: channelapi.Invite, Channel: "lib-x", Agent: visitor})
	f.must(channelapi.Request{AgentID: boss, Action: channelapi.RemoveMember, Channel: "agent-bus", Agent: member})
	if reply := f.must(channelapi.Request{AgentID: owner, Action: channelapi.Update, Channel: "agent-bus", Description: "bus bug reports"}); reply.Channel.Description != "bus bug reports" || !slices.Equal(reply.Channel.Members, []string{owner}) {
		t.Errorf("after the removal and the update the channel is %+v", reply.Channel)
	}
}

func TestTheCreationLimitCountsADayAndSparesTheBusOwner(t *testing.T) {
	f := newChannelFixture(t)
	start := f.now
	for _, name := range []string{"one", "two"} {
		if _, err := f.create(owner, name); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Hour)
	}
	_, err := f.create(owner, "three")
	next := start.Add(creationWindow).UTC().Format("2006-01-02T15:04Z")
	if want := "limit of 2 new channels per day reached; next at " + next; err == nil || err.Error() != want {
		t.Errorf("a third channel: %v, want %q", err, want)
	}
	f.now = start.Add(creationWindow)
	if reply, err := f.create(owner, "three"); err != nil || *reply.Limits.ChannelsLeftToday != 0 {
		t.Errorf("a day after the first one: %+v, %v", reply.Limits, err)
	}
	for _, name := range []string{"a", "b", "c"} {
		reply, err := f.create(boss, name)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Limits.ChannelsLeftToday != nil || reply.Limits.CreationLimitPerDay != nil {
			t.Errorf("the bus owner has a creation limit: %+v", reply.Limits)
		}
	}
}

func TestARequestMustBeSignedByTheCallerAsTheDirectoryKnowsIt(t *testing.T) {
	f := newChannelFixture(t)
	caPEM := mustRead(t, filepath.Join(f.dir, "ca.pem"))
	list := channelapi.Request{AgentID: owner, Action: channelapi.List}
	forged, _ := channelapi.Sign(list, f.keys[visitor])
	if _, err := channelapi.VerifyReply(caPEM, f.channels.HandleRequest(owner, forged), forged); err == nil || !strings.Contains(err.Error(), "not signed with the current key") {
		t.Errorf("a request signed with another agent's key: %v", err)
	}
	ownRequest, _ := channelapi.Sign(list, f.keys[owner])
	if _, err := channelapi.VerifyReply(caPEM, f.channels.HandleRequest(visitor, ownRequest), ownRequest); err == nil || !strings.Contains(err.Error(), "current key of "+visitor) {
		t.Errorf("a request on another agent's subject: %v", err)
	}
	stranger := f.newKey()
	f.keys["stranger@x"] = stranger
	if _, err := f.ask(channelapi.Request{AgentID: "stranger@x", Action: channelapi.List}); err == nil || !strings.Contains(err.Error(), "not in the directory") {
		t.Errorf("an agent the directory does not know: %v", err)
	}
	if _, err := f.ask(channelapi.Request{AgentID: owner, Action: "delete", Channel: "x"}); err == nil || !strings.Contains(err.Error(), "unknown channel action") {
		t.Errorf("an unknown action: %v", err)
	}
}

// agent-busctl removes an agent while its join waits for the lock: the join must not give the
// removed agent a cursor, though the published directory still lists it.
func TestARequestThatWaitedBehindARemovalIsRefused(t *testing.T) {
	f := newChannelFixture(t)
	if _, err := f.create(owner, "agent-bus"); err != nil {
		t.Fatal(err)
	}
	f.channels.config.LockMutations = func() (func(), error) {
		record := f.registry.records[visitor]
		removedAt := f.now
		record.RemovedAt = &removedAt
		f.registry.records[visitor] = record
		f.registry.accounts = slices.DeleteFunc(f.registry.accounts, func(account string) bool { return account == visitor })
		return func() {}, nil
	}
	_, err := f.ask(channelapi.Request{AgentID: visitor, Action: channelapi.Join, Channel: "agent-bus"})
	if err == nil || !strings.Contains(err.Error(), "not an agent of the bus") {
		t.Errorf("a join after the removal: %v", err)
	}
	if _, err := f.ask(channelapi.Request{AgentID: owner, Action: channelapi.Invite, Channel: "agent-bus", Agent: visitor}); err == nil || !strings.Contains(err.Error(), "not an agent of the bus") {
		t.Errorf("an invite of the removed agent: %v", err)
	}
	if slices.ContainsFunc(f.registry.cursors, func(cursor string) bool { return strings.HasPrefix(cursor, visitor+" channel") }) {
		t.Errorf("the removed agent got a cursor: %v", f.registry.cursors)
	}
}

func TestARequestSignedWithAReplacedKeyIsRefused(t *testing.T) {
	f := newChannelFixture(t)
	if _, err := f.create(owner, "agent-bus"); err != nil {
		t.Fatal(err)
	}
	f.register(visitor, f.newKey()) // rekeyed in the registry; the published directory has the old key
	if _, err := f.ask(channelapi.Request{AgentID: visitor, Action: channelapi.Join, Channel: "agent-bus"}); err == nil || !strings.Contains(err.Error(), "key of "+visitor+" changed") {
		t.Errorf("a join signed with the old key: %v", err)
	}
}

// A create that failed after its first steps is finished by the next create of the same owner,
// which keeps the channel's entry and the cursor's start and counts once against the limit.
func TestARepeatedCreateFinishesAHalfMadeChannel(t *testing.T) {
	f := newChannelFixture(t)
	f.registry.last = 7
	f.registry.createErr = errors.New("the reload was not confirmed")
	if _, err := f.create(owner, "agent-bus"); err == nil {
		t.Fatal("the create succeeded although the grants failed")
	}
	f.registry.createErr = nil
	f.registry.last = 9
	created := f.now
	f.now = f.now.Add(time.Minute)
	reply, err := f.create(owner, "agent-bus")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reply.Channel.Members, []string{owner}) || !slices.Contains(f.registry.cursors, owner+" channel agent-bus after 7") {
		t.Errorf("members %v, cursors %v: want the owner's cursor of the first attempt", reply.Channel.Members, f.registry.cursors)
	}
	if *reply.Limits.ChannelsLeftToday != 1 {
		t.Errorf("channels left = %d, want 1: the channel counts once", *reply.Limits.ChannelsLeftToday)
	}
	if entry := f.registry.groups[0]; entry.Owner != owner || !entry.CreatedAt.Equal(created.UTC()) {
		t.Errorf("the entry is %+v, want the first attempt's", entry)
	}
	if _, err := f.create(member, "agent-bus"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("another agent's create of the channel: %v", err)
	}
}

func TestAListStopsAtItsBound(t *testing.T) {
	f := newChannelFixture(t)
	for i := range channelapi.MaxListedChannels + 1 {
		f.registry.groups = append(f.registry.groups, bus.TopicRecord{Name: fmt.Sprintf("c%04d", i)})
	}
	reply := f.must(channelapi.Request{AgentID: member, Action: channelapi.List})
	if len(reply.Channels) != channelapi.MaxListedChannels || !reply.Truncated || reply.Channels[0].Name != "c0000" {
		t.Errorf("%d channels, truncated %v", len(reply.Channels), reply.Truncated)
	}
}

func TestChannelRequestsPastTheLimitAreAnsweredWithoutASignature(t *testing.T) {
	f := newChannelFixture(t)
	f.channels.limits = newCallerLimits(time.Nanosecond, 100, time.Hour, 1)
	f.must(channelapi.Request{AgentID: owner, Action: channelapi.List})
	if _, err := f.ask(channelapi.Request{AgentID: owner, Action: channelapi.List}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("a request past the limit: %v", err)
	}
	f.must(channelapi.Request{AgentID: member, Action: channelapi.List})
}
