package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// fakeBusd answers channel requests with what answer says, signed by a CA of its own like busd.
// It checks each request's signature against the test bus's directory.
type fakeBusd struct {
	t         *testing.T
	ca        *tlsinit.CA
	caPEM     []byte
	directory trust.FileDirectory
	answer    func(request channelapi.Request) channelapi.Reply
	down      error

	mu       sync.Mutex
	requests []channelapi.Request
}

func (b *testBus) startBusd(answer func(channelapi.Request) channelapi.Reply) *fakeBusd {
	dir := b.t.TempDir()
	err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "broker.pem"), BrokerKeyPath: filepath.Join(dir, "broker.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		b.t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		b.t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		b.t.Fatal(err)
	}
	b.busd = &fakeBusd{t: b.t, ca: ca, caPEM: caPEM, directory: trust.DirectoryPath(b.configDir), answer: answer}
	return b.busd
}

func (busd *fakeBusd) RequestChannel(ctx context.Context, message []byte) ([]byte, error) {
	if busd.down != nil {
		return nil, busd.down
	}
	var signed struct {
		Payload []byte `json:"payload"`
	}
	var claimed channelapi.Request
	if err := errors.Join(json.Unmarshal(message, &signed), json.Unmarshal(signed.Payload, &claimed)); err != nil {
		busd.t.Fatal(err)
	}
	record, err := busd.directory.Lookup(ctx, claimed.AgentID)
	if err != nil {
		busd.t.Fatal(err)
	}
	request, err := channelapi.Open(message, record)
	if err != nil {
		busd.t.Fatal(err)
	}
	busd.mu.Lock()
	busd.requests = append(busd.requests, request)
	busd.mu.Unlock()
	reply := busd.answer(request)
	reply.RequestHash = directory.RequestHash(message)
	return channelapi.SignReply(busd.ca, reply)
}

func ptr[T any](value T) *T { return &value }

func TestChannelsListsEveryChannelAndSaysWhichTheAgentReads(t *testing.T) {
	b := newTestBus(t, alice, bob)
	b.startBusd(func(channelapi.Request) channelapi.Reply {
		return channelapi.Reply{Channels: []channelapi.Channel{
			{Name: "agent-bus", Description: "bus bug reports", Owner: bob, Members: []string{alice, bob}},
			{Name: "lib-x", Description: "about lib-x", Members: []string{bob}},
		}}
	})
	client := b.connect(alice)

	listing := decode[channelsOut](t, call(t, client, "channels", nil))
	want := []channelEntry{
		{Name: "agent-bus", Description: "bus bug reports", Owner: bob, Joined: true},
		{Name: "lib-x", Description: "about lib-x", Owner: "bus owner", Joined: false},
	}
	if !slices.Equal(listing.Channels, want) || listing.Note != "" {
		t.Errorf("channels = %+v, want %+v", listing, want)
	}
	members := decode[channelsOut](t, call(t, client, "channels", map[string]any{"action": "members", "channel": "agent-bus"}))
	if members.Channel != "agent-bus" || members.Owner != bob || !slices.Equal(members.Members, []string{alice, bob}) {
		t.Errorf("members = %+v", members)
	}
}

func TestChannelsWithoutBusdListsTheLocalCatalogueWithANote(t *testing.T) {
	b := newTestBus(t, alice, bob)
	if err := trust.DirectoryPath(b.configDir).WriteCatalogue(trust.Catalogue{"dev": {alice}, "ops": {bob}}); err != nil {
		t.Fatal(err)
	}
	b.startBusd(nil).down = errors.New("busd is not running")
	client := b.connect(alice)

	listing := decode[channelsOut](t, call(t, client, "channels", nil))
	want := []channelEntry{{Name: "dev", Joined: true}, {Name: "ops"}}
	if !slices.Equal(listing.Channels, want) || !strings.Contains(listing.Note, "busd is not running") {
		t.Errorf("channels = %+v, want %+v with a note", listing, want)
	}
}

func TestChannelsCreateSaysTheLimitsInOneLine(t *testing.T) {
	b := newTestBus(t, alice)
	busd := b.startBusd(func(request channelapi.Request) channelapi.Reply {
		return channelapi.Reply{
			Channel: &channelapi.Channel{Name: request.Channel, Description: request.Description, Owner: request.AgentID, Members: []string{request.AgentID}},
			Limits:  &channelapi.Limits{MessageRetention: ptr("90d"), ChannelsLeftToday: ptr(4), CreationLimitPerDay: ptr(5)},
		}
	})
	client := b.connect(alice)

	created := decode[channelsOut](t, call(t, client, "channels", map[string]any{"action": "create", "channel": "agent-bus", "description": "bus bug reports"}))
	if want := "public channel agent-bus created, you own it; messages kept 90d; 4 of 5 channel creations left today"; created.Result != want || created.Owner != alice {
		t.Errorf("create = %+v, want result %q", created, want)
	}
	if got := busd.requests[0]; got.AgentID != alice || got.Action != channelapi.Create || got.Description != "bus bug reports" {
		t.Errorf("busd got %+v", got)
	}
}

func TestChannelsShowsBusdsRefusal(t *testing.T) {
	b := newTestBus(t, alice)
	b.startBusd(func(channelapi.Request) channelapi.Reply {
		return channelapi.Reply{Error: "limit of 5 new channels per day reached; next at 2026-11-26T09:14Z"}
	})
	client := b.connect(alice)

	result := call(t, client, "channels", map[string]any{"action": "create", "channel": "more", "description": "x"})
	if !result.IsError || !strings.Contains(errorText(result), "next at 2026-11-26T09:14Z") {
		t.Errorf("result = %+v", result.Content)
	}
}

func TestAnInvitedAgentGetsASignedNotice(t *testing.T) {
	b := newTestBus(t, alice, bob)
	b.startBusd(func(request channelapi.Request) channelapi.Reply {
		return channelapi.Reply{Channel: &channelapi.Channel{Name: request.Channel, Description: "bus bug reports", Owner: alice, Members: []string{alice, request.Agent}}}
	})
	a, bobClient := b.connect(alice), b.connect(bob)

	invited := decode[channelsOut](t, call(t, a, "channels", map[string]any{"action": "invite", "channel": "agent-bus", "agent": bob}))
	if invited.Warning != "" || !slices.Equal(invited.Members, []string{alice, bob}) {
		t.Errorf("invite = %+v", invited)
	}
	received := decode[messagesOut](t, call(t, bobClient, "wait_for_messages", map[string]any{"timeout_s": 10}))
	if len(received.Messages) != 1 {
		t.Fatalf("bob got %+v", received)
	}
	notice := received.Messages[0]
	if notice.From != alice || notice.Type != "status" || !notice.TargetsMe || !strings.Contains(notice.Text, "added you to channel agent-bus (bus bug reports)") {
		t.Errorf("notice = %+v", notice)
	}
}
