//go:build integration

// Package itest brings up a real broker with deploy/broker.sh and checks milestone 1 end to end.
// Run it with `make itest`; it needs podman and the pinned Redpanda image.
package itest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

const (
	alice = "api-client@alice"
	bob   = "lib-x@bob"

	operationTimeout = 20 * time.Second
	brokerScript     = "../../deploy/broker.sh"
)

// harness is a running broker with an initialised config directory.
type harness struct {
	configDir string
	address   string
	busctl    string
	env       []string
}

func startBus(t *testing.T) *harness {
	t.Helper()
	port := freePort(t)
	configDir := t.TempDir()
	busctl := filepath.Join(t.TempDir(), "busctl")
	podName := fmt.Sprintf("agent-bus-itest-%d", os.Getpid())

	h := &harness{
		configDir: configDir,
		address:   fmt.Sprintf("localhost:%d", port),
		busctl:    busctl,
		env: append(os.Environ(),
			"BUS_CONFIG_DIR="+configDir,
			"BUS_BROKER=localhost:"+fmt.Sprint(port),
			"BUS_POD_NAME="+podName,
			"BUS_KAFKA_PORT="+fmt.Sprint(port),
		),
	}
	// Stop the pod however the test ends, and also after a start that failed half way.
	t.Cleanup(func() { h.run(t, brokerScript, "down", "--purge") })

	h.run(t, "go", "build", "-o", busctl, "../../cmd/busctl")
	h.run(t, busctl, "tls", "init")
	h.run(t, busctl, "admin", "init")
	h.run(t, brokerScript, "up")
	h.run(t, busctl, "init")
	h.run(t, busctl, "register", alice)
	h.run(t, busctl, "register", bob)
	h.run(t, busctl, "topic", "create", "lib-x", "--description", "discussion about lib-x")
	return h
}

func (b *harness) run(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := b.tryRun(name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, output)
	}
	return output
}

func (b *harness) tryRun(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = b.env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func (b *harness) connection(user string, credentialsDir string) broker.Connection {
	password, err := creds.ReadPassword(credentialsDir)
	if err != nil {
		panic(err)
	}
	return broker.Connection{
		Address:    b.address,
		CACertPath: filepath.Join(b.configDir, "ca.pem"),
		User:       user,
		Password:   password,
	}
}

func (b *harness) admin(t *testing.T) *broker.Admin {
	t.Helper()
	admin, err := broker.NewAdmin(b.connection(creds.AdminUser, creds.AdminDir(b.configDir)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	return admin
}

// agentClient is a client with the agent's SCRAM credentials; opts add consumer or producer settings.
func (b *harness) agentClient(t *testing.T, agentID string, opts ...kgo.Opt) *kgo.Client {
	t.Helper()
	client, err := broker.NewClient(b.connection(agentID, creds.AgentDir(b.configDir, agentID)), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestMilestone1(t *testing.T) {
	h := startBus(t)

	t.Run("a client without TLS cannot connect", func(t *testing.T) {
		client, err := kgo.NewClient(
			kgo.SeedBrokers(h.address),
			kgo.SASL(scram.Auth{User: "x", Pass: "x"}.AsSha256Mechanism()),
			kgo.RequestRetries(0),
		)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		if err := client.Ping(ctx); err == nil {
			t.Fatal("plaintext connection to the broker succeeded")
		}
	})

	t.Run("a client that does not trust the bus CA cannot connect", func(t *testing.T) {
		password, _ := creds.ReadPassword(creds.AdminDir(h.configDir))
		client, err := kgo.NewClient(
			kgo.SeedBrokers(h.address),
			kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
			kgo.SASL(scram.Auth{User: creds.AdminUser, Pass: password}.AsSha256Mechanism()),
			kgo.RequestRetries(0),
		)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		if err := client.Ping(ctx); err == nil {
			t.Fatal("connection with the system trust store succeeded")
		}
	})

	t.Run("an agent reads its own DM topic and is denied the other's", func(t *testing.T) {
		// bob writes to alice's DM topic: any agent may write any DM topic.
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		sender := h.agentClient(t, bob, kgo.DisableIdempotentWrite())
		message := &kgo.Record{Topic: bus.DMTopic(alice), Value: []byte("hello alice")}
		if err := sender.ProduceSync(ctx, message).FirstErr(); err != nil {
			t.Fatalf("bob cannot write to alice's DM topic: %v", err)
		}

		reader := h.agentClient(t, alice,
			kgo.ConsumerGroup(alice),
			kgo.ConsumeTopics(bus.DMTopic(alice)),
			kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		)
		fetches := reader.PollFetches(ctx)
		if err := fetches.Err(); err != nil {
			t.Fatalf("alice cannot read her own DM topic: %v", err)
		}
		if got := fetches.Records(); len(got) != 1 || string(got[0].Value) != "hello alice" {
			t.Fatalf("alice read %v, want the one message from bob", got)
		}

		spy := h.agentClient(t, alice,
			kgo.ConsumeTopics(bus.DMTopic(bob)),
			kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		)
		spied := spy.PollFetches(ctx)
		if !hasError(spied, kerr.TopicAuthorizationFailed) {
			t.Fatalf("alice reading bob's DM topic: errors %v, want TOPIC_AUTHORIZATION_FAILED", spied.Errors())
		}
	})

	t.Run("an agent cannot write the registry", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		client := h.agentClient(t, alice, kgo.DisableIdempotentWrite())
		err := client.ProduceSync(ctx, &kgo.Record{Topic: bus.RegistryTopic, Key: []byte(alice), Value: []byte("{}")}).FirstErr()
		if !errors.Is(err, kerr.TopicAuthorizationFailed) {
			t.Fatalf("write to the registry: %v, want TOPIC_AUTHORIZATION_FAILED", err)
		}
	})

	t.Run("topics are not auto-created", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		admin := h.admin(t)
		const unknown = "agents.not-created"
		client, err := broker.NewClient(h.connection(creds.AdminUser, creds.AdminDir(h.configDir)),
			kgo.UnknownTopicRetries(0), kgo.DisableIdempotentWrite())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		if err := client.ProduceSync(ctx, &kgo.Record{Topic: unknown, Value: []byte("x")}).FirstErr(); err == nil {
			t.Fatal("producing to an unknown topic succeeded")
		}
		topics, err := admin.ListTopics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if topics.Has(unknown) {
			t.Fatalf("%s was auto-created", unknown)
		}
	})

	t.Run("system, DM and group topics have the designed layout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		admin := h.admin(t)
		topics, err := admin.ListTopics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, topic := range []string{
			bus.RegistryTopic, bus.GeneralTopic, bus.RequestsTopic, bus.TopicsTopic,
			bus.DMTopic(alice), bus.DMTopic(bob), bus.GroupTopic("lib-x"),
		} {
			detail, ok := topics[topic]
			if !ok || len(detail.Partitions) != 1 {
				t.Errorf("%s: present %v with %d partitions, want 1", topic, ok, len(detail.Partitions))
			}
		}
		configs, err := admin.DescribeTopicConfigs(ctx, bus.RegistryTopic, bus.DMTopic(alice))
		if err != nil {
			t.Fatal(err)
		}
		if got := configValue(t, configs, bus.RegistryTopic, "cleanup.policy"); got != "compact" {
			t.Errorf("registry cleanup.policy = %q, want compact", got)
		}
		if got := configValue(t, configs, bus.DMTopic(alice), "retention.ms"); got != "-1" {
			t.Errorf("DM topic retention.ms = %q, want -1", got)
		}
	})

	t.Run("the registry and catalogue hold the registered entries", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		reader := h.agentClient(t, alice,
			kgo.ConsumeTopics(bus.RegistryTopic, bus.TopicsTopic),
			kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		)
		seen := map[string]string{}
		for len(seen) < 3 {
			fetches := reader.PollFetches(ctx)
			if err := fetches.Err(); err != nil {
				t.Fatal(err)
			}
			fetches.EachRecord(func(record *kgo.Record) { seen[record.Topic+"/"+string(record.Key)] = string(record.Value) })
		}
		var registered bus.AgentRecord
		if err := json.Unmarshal([]byte(seen[bus.RegistryTopic+"/"+bob]), &registered); err != nil || registered.AgentID != bob || registered.Fingerprint == "" {
			t.Errorf("registry entry of %s = %q (%v)", bob, seen[bus.RegistryTopic+"/"+bob], err)
		}
		var topic bus.TopicRecord
		if err := json.Unmarshal([]byte(seen[bus.TopicsTopic+"/lib-x"]), &topic); err != nil || topic.Description != "discussion about lib-x" {
			t.Errorf("catalogue entry of lib-x = %q (%v)", seen[bus.TopicsTopic+"/lib-x"], err)
		}
	})

	t.Run("the broker reports an unknown SCRAM user as absent and a registered one as present", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		admin := h.admin(t)
		for user, want := range map[string]bool{alice: true, "nobody@nowhere": false} {
			got, err := admin.HasSCRAMUser(ctx, user)
			if err != nil || got != want {
				t.Errorf("HasSCRAMUser(%s) = (%v, %v), want (%v, nil)", user, got, err, want)
			}
		}
	})

	t.Run("registering an existing agent fails", func(t *testing.T) {
		if output, err := h.tryRun(h.busctl, "register", alice); err == nil {
			t.Fatalf("second register succeeded:\n%s", output)
		}
	})
}

func configValue(t *testing.T, configs kadm.ResourceConfigs, topic, name string) string {
	t.Helper()
	for _, resource := range configs {
		if resource.Name != topic {
			continue
		}
		for _, config := range resource.Configs {
			if config.Key == name {
				return config.MaybeValue()
			}
		}
	}
	t.Fatalf("%s has no config %s", topic, name)
	return ""
}

func hasError(fetches kgo.Fetches, want error) bool {
	for _, fetchErr := range fetches.Errors() {
		if errors.Is(fetchErr.Err, want) {
			return true
		}
	}
	return false
}
