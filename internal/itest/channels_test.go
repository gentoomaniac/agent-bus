//go:build integration

package itest

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/adapter"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// channelAgent runs the adapter of a registered agent as `agent-bus mcp` does: with busd for its
// directory and its channel requests.
func (h *harness) channelAgent(t *testing.T, agentID string) *adapter.Adapter {
	t.Helper()
	caPEM, err := os.ReadFile(h.caCert)
	if err != nil {
		t.Fatal(err)
	}
	transport := h.transport(t, agentID)
	session, err := adapter.Open(adapter.Config{
		AgentID: agentID, Dir: creds.AgentDir(h.configDir, agentID), Transport: transport, Directory: trust.DirectoryPath(h.configDir),
		DirectoryLink: transport.(adapter.DirectoryLink), ChannelLink: transport.(adapter.ChannelLink), CACert: caPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.RunDirectory(ctx, time.Hour, func(err error) { t.Logf("%s syncing the directory: %v", agentID, err) })
	}()
	t.Cleanup(func() { cancel(); <-done; session.Close() })
	return session
}

func TestAgentsCreateJoinAndManageChannelsThroughBusd(t *testing.T) {
	h := startBus(t)
	const carol = "carol@alice"
	h.register(t, carol)
	h.run(t, h.agentBusctl, "enroll", "init")
	h.env = append(h.env, "BUSD_CHANNEL_CREATE_LIMIT=2")
	h.startBusd(t)
	aliceSession, bobSession, carolSession := h.channelAgent(t, alice), h.channelAgent(t, bob), h.channelAgent(t, carol)
	request := func(session *adapter.Adapter, request channelapi.Request) (channelapi.Reply, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return session.RequestChannel(ctx, request)
	}
	waitFor(t, "busd to know the agents", func() bool {
		_, err := request(carolSession, channelapi.Request{Action: channelapi.List})
		return err == nil
	})

	t.Run("an agent creates a channel and another joins it without a restart", func(t *testing.T) {
		created, err := request(aliceSession, channelapi.Request{Action: channelapi.Create, Channel: "agent-bus", Description: "bus bug reports"})
		if err != nil {
			t.Fatal(err)
		}
		if created.Channel.Owner != alice || !slices.Equal(created.Channel.Members, []string{alice}) || *created.Limits.ChannelsLeftToday != 1 {
			t.Errorf("created %+v with limits %+v", created.Channel, created.Limits)
		}
		if _, err := request(bobSession, channelapi.Request{Action: channelapi.Join, Channel: "agent-bus"}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "bob to read agent-bus", func() bool { return slices.Contains(bobSession.Channels(), bus.GroupChannel("agent-bus")) })
		if _, err := aliceSession.Publish(timeout(t), "agent-bus", sayTo("welcome to #agent-bus", bob)); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, bobSession, 1); got[0].Envelope.Body.Text != "welcome to #agent-bus" {
			t.Errorf("bob got %v", summaries(got))
		}
	})

	t.Run("the channel owner invites an agent, which is told and reads the channel", func(t *testing.T) {
		reply, warning, err := aliceSession.Invite(timeout(t), "agent-bus", carol)
		if err != nil || warning != "" {
			t.Fatalf("invite: %v, warning %q", err, warning)
		}
		if !slices.Contains(reply.Channel.Members, carol) {
			t.Errorf("members after the invite: %v", reply.Channel.Members)
		}
		got := pollUntil(t, carolSession, 1)
		if notice := got[0].Envelope; notice.From != alice || notice.Type != "status" || !strings.Contains(notice.Body.Text, "added you to channel agent-bus") {
			t.Errorf("carol got %v", summaries(got))
		}
		waitFor(t, "carol to read agent-bus", func() bool { return slices.Contains(carolSession.Channels(), bus.GroupChannel("agent-bus")) })
	})

	t.Run("an agent that does not own the channel may not invite or remove", func(t *testing.T) {
		for _, refused := range []channelapi.Request{
			{Action: channelapi.Invite, Channel: "agent-bus", Agent: carol},
			{Action: channelapi.RemoveMember, Channel: "agent-bus", Agent: carol},
			{Action: channelapi.Invite, Channel: "lib-x", Agent: carol}, // agent-busctl's: the bus owner's
		} {
			if _, err := request(bobSession, refused); err == nil || !strings.Contains(err.Error(), "only the owner") {
				t.Errorf("bob %s %s: %v", refused.Action, refused.Channel, err)
			}
		}
		if holders, err := h.admin(t).ReadCursorHolders(timeout(t), bus.GroupChannel("lib-x")); err != nil || slices.Contains(holders, carol) {
			t.Errorf("lib-x readers %v, %v", holders, err)
		}
		// Nor ask in the channel owner's name: only on its own subject.
		h.rawAgent(t, bob).denyPublish(t, channelapi.Subject(bus.AgentToken(alice)))
	})

	t.Run("the creation limit refuses a third channel in a day and says when", func(t *testing.T) {
		if _, err := request(aliceSession, channelapi.Request{Action: channelapi.Create, Channel: "second", Description: "two"}); err != nil {
			t.Fatal(err)
		}
		_, err := request(aliceSession, channelapi.Request{Action: channelapi.Create, Channel: "third", Description: "three"})
		if err == nil || !strings.Contains(err.Error(), "limit of 2 new channels per day reached; next at ") {
			t.Errorf("a third channel: %v", err)
		}
		if _, err := request(bobSession, channelapi.Request{Action: channelapi.Create, Channel: "third", Description: "bob's first"}); err != nil {
			t.Errorf("another agent's first channel: %v", err)
		}
	})

	t.Run("agent-busctl channel create keeps an agent's ownership", func(t *testing.T) {
		h.run(t, h.agentBusctl, "--direct", "channel", "create", "agent-bus", "--description", "renamed by the bus owner")
		list, err := request(carolSession, channelapi.Request{Action: channelapi.List})
		if err != nil {
			t.Fatal(err)
		}
		index := slices.IndexFunc(list.Channels, func(channel channelapi.Channel) bool { return channel.Name == "agent-bus" })
		if index < 0 || list.Channels[index].Owner != alice || list.Channels[index].Description != "renamed by the bus owner" {
			t.Errorf("list = %+v", list.Channels)
		}
	})
}
