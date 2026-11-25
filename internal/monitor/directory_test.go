package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

// heads stands in for busd's directory.head: the monitor's handler, and a CA to sign with.
type heads struct {
	t     *testing.T
	ca    *tlsinit.CA
	caPEM []byte

	mu      sync.Mutex
	handler func([]byte)
}

func newHeads(t *testing.T) *heads {
	t.Helper()
	dir := t.TempDir()
	err := tlsinit.Create(tlsinit.Options{
		CACertPath: filepath.Join(dir, "ca.pem"), CAKeyPath: filepath.Join(dir, "ca.key"),
		BrokerCertPath: filepath.Join(dir, "b.pem"), BrokerKeyPath: filepath.Join(dir, "b.key"),
		SANs: []string{"localhost"}, BrokerValidity: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := tlsinit.LoadCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	return &heads{t: t, ca: ca, caPEM: caPEM}
}

func (heads *heads) watch(handler func([]byte)) (func(), error) {
	heads.mu.Lock()
	defer heads.mu.Unlock()
	heads.handler = handler
	return func() {}, nil
}

func (heads *heads) announce(revision uint64) {
	heads.t.Helper()
	head, err := directory.SignHead(heads.ca, directory.Version{Epoch: "e", Revision: revision, StateHash: "h"}, nil)
	if err != nil {
		heads.t.Fatal(err)
	}
	heads.mu.Lock()
	handler := heads.handler
	heads.mu.Unlock()
	handler(head)
}

func TestAHeadRefreshesTheAgentsAtOnce(t *testing.T) {
	alice := newTestAgent("alice@x")
	fake := newFakeBus(t, alice)
	heads := newHeads(t)
	monitor := New(Config{Admin: fake, History: fake, Report: func(err error) { t.Error(err) }, DirectoryHeads: heads.watch, CACert: heads.caPEM})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, "the first round", func() bool { return len(monitor.Agents()) == 1 })
	waitUntil(t, "the head subscription", func() bool { heads.mu.Lock(); defer heads.mu.Unlock(); return heads.handler != nil })

	fake.mu.Lock()
	fake.agents = append(fake.agents, bus.AgentRecord{AgentID: "carol@x"})
	fake.mu.Unlock()
	started := time.Now()
	heads.announce(2)

	waitUntil(t, "carol in the agent list", func() bool {
		return slices.ContainsFunc(monitor.Agents(), func(agent Agent) bool { return agent.ID == "carol@x" })
	})
	if elapsed := time.Since(started); elapsed >= discoverInterval {
		t.Errorf("the head took %s, as long as a round", elapsed)
	}
	if status := monitor.Directory(); status.Version != "e/2" || status.Error != "" {
		t.Errorf("directory status %+v", status)
	}
}

func TestAHeadNotSignedByTheCAIsIgnored(t *testing.T) {
	fake := newFakeBus(t)
	heads, impostor := newHeads(t), newHeads(t)
	monitor := New(Config{Admin: fake, History: fake, Report: func(error) {}, CACert: heads.caPEM})
	monitor.config.DirectoryHeads = impostor.watch
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, "the head subscription", func() bool {
		impostor.mu.Lock()
		defer impostor.mu.Unlock()
		return impostor.handler != nil
	})

	impostor.announce(7)

	if status := monitor.Directory(); status.Version != "" {
		t.Errorf("a forged head was taken: %+v", status)
	}
}

func TestAFailedDirectoryReadIsShownApartFromPresence(t *testing.T) {
	fake := &failingRegistry{fakeBus: newFakeBus(t)}
	monitor := New(Config{Admin: fake, History: fake, Report: func(error) {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { monitor.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitUntil(t, "the failure in the status", func() bool { return monitor.Status().Directory.Error == "busd is down" })
}

type failingRegistry struct{ *fakeBus }

func (*failingRegistry) Agents(context.Context) ([]bus.AgentRecord, error) {
	return nil, errors.New("busd is down")
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
