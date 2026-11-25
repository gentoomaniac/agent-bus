package enrollclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// directoryBusd answers busd.directory with a scripted reply.
type directoryBusd struct {
	reply enroll.DirectoryReply
	err   error
}

func (fake *directoryBusd) Request(_ context.Context, _ string, _ []byte) ([]byte, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	return json.Marshal(fake.reply)
}

// authority is a CA like busd's, with its certificate as an agent has it in ca.pem.
type authority struct {
	ca    *tlsinit.CA
	caPEM []byte
}

func newAuthority(t *testing.T) authority {
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
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return authority{ca: ca, caPEM: caPEM}
}

func (a authority) signed(t *testing.T, issuedAt time.Time, records []bus.AgentRecord, channels map[string][]string) enroll.DirectoryReply {
	t.Helper()
	reply, err := enroll.SignDirectory(a.ca, enroll.Directory{Records: records, Channels: channels}, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func record(agentID string) bus.AgentRecord {
	return bus.AgentRecord{AgentID: agentID, PublicKey: "key-of-" + agentID}
}

var now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func merge(busd *directoryBusd, dir trust.FileDirectory, caPEM []byte) error {
	return mergeDirectory(context.Background(), busd, dir, caPEM, now)
}

func TestMergingASignedDirectoryAddsAndReplacesButNeverDeletes(t *testing.T) {
	ca := newAuthority(t)
	dir := trust.DirectoryPath(t.TempDir())
	for _, agentID := range []string{"old@x", "alice@x"} {
		if err := dir.Write(record(agentID)); err != nil {
			t.Fatal(err)
		}
	}
	removed := record("alice@x")
	removedAt := now
	removed.RemovedAt = &removedAt
	busd := &directoryBusd{reply: ca.signed(t, now, []bus.AgentRecord{removed, record("new@x")}, map[string][]string{"team": {"new@x"}})}

	if err := merge(busd, dir, ca.caPEM); err != nil {
		t.Fatal(err)
	}

	records, err := dir.Records(context.Background())
	if err != nil || len(records) != 3 {
		t.Fatalf("records after the merge: %+v, %v; want old, alice and new", records, err)
	}
	if records[0].AgentID != "alice@x" || !records[0].Removed() {
		t.Errorf("alice's removal did not arrive: %+v", records[0])
	}
	if catalogue, _ := dir.Catalogue(); len(catalogue["team"]) != 1 {
		t.Errorf("catalogue after the merge: %v", catalogue)
	}
	if issuedAt, _ := dir.IssuedAt(); !issuedAt.Equal(now) {
		t.Errorf("issued_at = %v, want %v", issuedAt, now)
	}
}

func TestADirectoryThatCannotBeTrustedWritesNothing(t *testing.T) {
	ca, otherCA := newAuthority(t), newAuthority(t)
	good := []bus.AgentRecord{record("good@x")}
	tampered := ca.signed(t, now, good, nil)
	tampered.Payload = []byte(string(tampered.Payload[:len(tampered.Payload)-1]) + ` ,"x":1}`)
	forged := ca.signed(t, now, good, nil)
	forged.Payload = []byte(`{"records":[{"agent_id":"good@x","public_key":"the attacker's"}],"channels":null,"issued_at":"` + now.Format(time.RFC3339) + `"}`)

	cases := map[string]*directoryBusd{
		"busd is unreachable":   {err: errors.New("no responders")},
		"busd reports an error": {reply: enroll.DirectoryReply{Error: "the registry cannot be read"}},
		"the reply is unsigned": {reply: enroll.DirectoryReply{Payload: ca.signed(t, now, good, nil).Payload}},
		"a forged record":       {reply: forged},
		"a tampered payload":    {reply: tampered},
		"another CA signed it":  {reply: otherCA.signed(t, now, good, nil)},
		"it is from the future": {reply: ca.signed(t, now.Add(time.Hour), good, nil)},
		"a record names a path": {reply: ca.signed(t, now, []bus.AgentRecord{record("good@x"), record("../evil@x")}, nil)},
		"a reader is invalid":   {reply: ca.signed(t, now, good, map[string][]string{"team": {"not an id"}})},
		"a channel is invalid":  {reply: ca.signed(t, now, good, map[string][]string{"Bad Name": nil})},
	}
	for name, busd := range cases {
		t.Run(name, func(t *testing.T) {
			dir := trust.DirectoryPath(t.TempDir())
			if err := merge(busd, dir, ca.caPEM); err == nil {
				t.Fatal("the merge succeeded")
			}
			if records, _ := dir.Records(context.Background()); len(records) != 0 {
				t.Errorf("records were written: %+v", records)
			}
			if issuedAt, _ := dir.IssuedAt(); !issuedAt.IsZero() {
				t.Errorf("issued_at was written: %v", issuedAt)
			}
		})
	}
}

func TestAReplayedOlderDirectoryIsRefused(t *testing.T) {
	ca := newAuthority(t)
	dir := trust.DirectoryPath(t.TempDir())
	older := ca.signed(t, now.Add(-time.Hour), []bus.AgentRecord{record("stale@x")}, nil)
	newer := ca.signed(t, now, []bus.AgentRecord{record("fresh@x")}, nil)

	if err := merge(&directoryBusd{reply: newer}, dir, ca.caPEM); err != nil {
		t.Fatal(err)
	}
	if err := merge(&directoryBusd{reply: older}, dir, ca.caPEM); err == nil {
		t.Error("an older directory was taken in after a newer one")
	}
	if _, err := dir.Lookup(context.Background(), "stale@x"); err == nil {
		t.Error("the replayed directory was written")
	}
	if err := merge(&directoryBusd{reply: newer}, dir, ca.caPEM); err != nil {
		t.Errorf("the same directory again: %v", err)
	}
}

func TestFetchingTheDirectoryNeedsTheEnrollmentPassword(t *testing.T) {
	configDir := t.TempDir()
	err := FetchDirectory(context.Background(), DirectoryOptions{ConfigDir: configDir})
	if !errors.Is(err, ErrNoEnrollPassword) {
		t.Errorf("without a password: %v, want ErrNoEnrollPassword", err)
	}
	if err := creds.WritePassword(filepath.Join(configDir, passwordDir), "", true); err != nil {
		t.Fatal(err)
	}
	if err := FetchDirectory(context.Background(), DirectoryOptions{ConfigDir: configDir}); !errors.Is(err, ErrNoEnrollPassword) {
		t.Errorf("with an empty password: %v, want ErrNoEnrollPassword", err)
	}
}
