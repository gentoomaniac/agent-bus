//go:build integration

package itest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
	"github.com/nats-io/nats.go"
)

// enrollMachine enrolls agentID on a new machine with the owner's approval and returns that
// machine's config directory.
func (h *harness) enrollMachine(t *testing.T, agentBus, agentID string) string {
	t.Helper()
	e := h.startEnrollee(t, agentBus, agentID, "--waitforcert", "--ca-fingerprint", h.caFingerprint(t))
	h.run(t, h.agentBusctl, "ca", "sign", h.pendingCode(t, agentID))
	if err := e.wait(t); err != nil {
		t.Fatalf("enroll failed: %v\n%s", err, e.output)
	}
	return e.configDir
}

// signedPayload decodes the payload of a message signed by the CA, without checking it: the test
// only looks at what was sent.
func signedPayload(t *testing.T, data []byte, payload any) {
	t.Helper()
	var signed directory.Signed
	if err := json.Unmarshal(data, &signed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(signed.Payload, payload); err != nil {
		t.Fatal(err)
	}
}

func TestARunningAgentLearnsOfANewOneFromTheHeadWithoutARestart(t *testing.T) {
	h := startBus(t)
	h.run(t, h.agentBusctl, "enroll", "init")
	h.startBusd(t)
	agentBus := h.buildAgentBus(t)
	const dave = "dave@dave"
	caPEM, err := os.ReadFile(h.caCert)
	if err != nil {
		t.Fatal(err)
	}

	carolDir := h.enrollMachine(t, agentBus, carol)
	carolSession := h.agentAt(t, carolDir, carol)
	carolDirectory := trust.DirectoryPath(carolDir)
	waitFor(t, "carol's first sync", func() bool {
		_, err := carolDirectory.Lookup(context.Background(), alice)
		return err == nil
	})

	// What busd sends: its heads to everyone, and its sync replies into carol's inbox.
	spy := h.rawAdmin(t, "bus-admin")
	heads := make(chan *nats.Msg, 64)
	replies := make(chan *nats.Msg, 64)
	if _, err := spy.conn.ChanSubscribe(directory.SubjectHead, heads); err != nil {
		t.Fatal(err)
	}
	if _, err := spy.conn.ChanSubscribe("_INBOX."+bus.AgentToken(carol)+".>", replies); err != nil {
		t.Fatal(err)
	}
	spy.conn.Flush()

	daveDir := h.enrollMachine(t, agentBus, dave)

	t.Run("busd announces the enrollment in a signed head", func(t *testing.T) {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case message := <-heads:
				head, err := directory.VerifyHead(caPEM, message.Data)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(head.Changes, directory.Change{Kind: directory.Joined, AgentID: dave}) {
					return
				}
			case <-deadline:
				t.Fatal("no head announced dave")
			}
		}
	})

	t.Run("the running agent syncs a diff on the head and knows the new agent", func(t *testing.T) {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case message := <-replies:
				var reply directory.SyncReply
				signedPayload(t, message.Data, &reply)
				if reply.Kind != directory.Diff {
					continue
				}
				if !slices.ContainsFunc(reply.Records, func(record bus.AgentRecord) bool { return record.AgentID == dave }) {
					t.Errorf("the diff holds %d records, not dave's", len(reply.Records))
				}
				waitFor(t, "dave in carol's directory", func() bool {
					_, err := carolDirectory.Lookup(context.Background(), dave)
					return err == nil
				})
				return
			case <-deadline:
				t.Fatal("carol got no diff")
			}
		}
	})

	t.Run("the two agents write to each other without a restart", func(t *testing.T) {
		daveSession := h.agentAt(t, daveDir, dave)
		if _, err := carolSession.Send(timeout(t), dave, say("welcome, dave")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, daveSession, 1); got[0].Envelope.From != carol {
			t.Errorf("dave got %v", summaries(got))
		}
		if _, err := daveSession.Send(timeout(t), carol, say("thanks")); err != nil {
			t.Fatal(err)
		}
		if got := pollUntil(t, carolSession, 1); got[0].Envelope.Body.Text != "thanks" {
			t.Errorf("carol got %v", summaries(got))
		}
	})

	t.Run("the snapshot and its exports are one machine's directory", func(t *testing.T) {
		snapshot, err := directory.Store{Dir: carolDirectory}.Load()
		if err != nil || !slices.ContainsFunc(snapshot.Records, func(record bus.AgentRecord) bool { return record.AgentID == dave }) {
			t.Errorf("carol's snapshot at %s lacks dave: %v", snapshot.Version, err)
		}
		if _, err := os.Stat(filepath.Join(carolDir, "enroll")); err == nil {
			t.Error("enroll kept the shared password on the machine")
		}
	})
}

func TestTheDirectoryGrantsAndRepliesStayInTheirPlace(t *testing.T) {
	h := startBus(t)
	h.run(t, h.agentBusctl, "enroll", "init")
	h.startBusd(t)
	bobAgent := h.rawAgent(t, bob)

	t.Run("an agent may ask for the directory and hear heads, but not announce one", func(t *testing.T) {
		bobAgent.expectNoViolation(t, "a sync request", func() {
			ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
			defer cancel()
			request := directory.NewSyncRequest(directory.Version{})
			answer, err := bobAgent.conn.RequestWithContext(ctx, directory.SyncSubject(bus.AgentToken(bob)), request)
			if err != nil {
				t.Fatal(err)
			}
			caPEM, _ := os.ReadFile(h.caCert)
			if reply, err := directory.VerifySyncReply(caPEM, answer.Data, request); err != nil || reply.Kind != directory.Full {
				t.Errorf("busd answered %+v, %v", reply.Kind, err)
			}
			if _, err := bobAgent.conn.SubscribeSync(directory.SubjectHead); err != nil {
				t.Fatal(err)
			}
		})
		bobAgent.denyPublish(t, directory.SubjectHead)
		// Only on its own sync subject: another agent's would let it use up that one's limit.
		bobAgent.denyPublish(t, directory.SyncSubject(bus.AgentToken(alice)))
		bobAgent.denyPublish(t, directory.SubjectSync)
	})

	t.Run("busd answers nowhere but into an agent's inbox", func(t *testing.T) {
		admin := h.admin(t)
		before, err := admin.LastSequence(timeout(t))
		if err != nil {
			t.Fatal(err)
		}
		spy := h.rawAdmin(t, "bus-admin")
		for _, reply := range []string{"general", "dm." + bus.AgentToken(alice), "_INBOX.>", "_INBOX." + bus.AgentToken(bob) + ".*"} {
			if err := spy.conn.PublishRequest(directory.SyncSubject(bus.AgentToken(bob)), reply, directory.NewSyncRequest(directory.Version{})); err != nil {
				t.Fatal(err)
			}
		}
		spy.conn.Flush()
		time.Sleep(time.Second)
		if after, err := admin.LastSequence(timeout(t)); err != nil || after != before {
			t.Errorf("BUS went from sequence %d to %d (%v): busd answered onto a message subject", before, after, err)
		}
	})
}

// The path an owner takes with a new machine: `agent-bus enroll`, then the first `agent-bus mcp`,
// with nothing copied or configured in between.
func TestAFreshlyEnrolledMachineKnowsTheDirectoryAtItsFirstStart(t *testing.T) {
	h := startBus(t)
	h.run(t, h.agentBusctl, "enroll", "init")
	h.startBusd(t)
	agentBus := h.buildAgentBus(t)
	machine := h.enrollMachine(t, agentBus, carol)
	aliceSession, _ := h.startAdapter(t, alice)

	cmd := exec.Command(agentBus, "mcp", "--agent", carol)
	cmd.Env = append(slices.Clone(h.env), "BUS_CONFIG_DIR="+machine) // the last value of a key counts
	carolMCP := h.connectMCP(t, cmd)

	var agents string
	waitFor(t, "alice in carol's list_agents", func() bool {
		agents, _ = carolMCP.call(t, "list_agents", nil)
		return strings.Contains(agents, alice)
	})
	if text, isError := carolMCP.call(t, "send", map[string]any{"to": alice, "type": "message", "body": map[string]any{"text": "hello from a new machine"}}); isError {
		t.Fatalf("send: %s", text)
	}
	if got := pollUntil(t, aliceSession, 1); got[0].Envelope.From != carol {
		t.Errorf("alice got %v", summaries(got))
	}
	if inbox, _ := carolMCP.call(t, "inbox", nil); !strings.Contains(inbox, "Directory loaded") {
		t.Errorf("the first inbox has no directory summary: %s", inbox)
	}
}
