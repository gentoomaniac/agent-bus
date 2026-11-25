package enrollclient

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// directoryBusd answers busd.directory with a scripted reply.
type directoryBusd struct {
	reply enroll.DirectoryReply
	err   error
	asked []string
}

func (fake *directoryBusd) Request(_ context.Context, subject string, _ []byte) ([]byte, error) {
	fake.asked = append(fake.asked, subject)
	if fake.err != nil {
		return nil, fake.err
	}
	return json.Marshal(fake.reply)
}

func record(agentID string) bus.AgentRecord {
	return bus.AgentRecord{AgentID: agentID, PublicKey: "key-of-" + agentID}
}

func TestMergingTheDirectoryAddsAndReplacesButNeverDeletes(t *testing.T) {
	dir := trust.DirectoryPath(t.TempDir())
	if err := dir.Write(record("old@x")); err != nil {
		t.Fatal(err)
	}
	if err := dir.Write(record("alice@x")); err != nil {
		t.Fatal(err)
	}
	removed := record("alice@x")
	removedAt := time.Now()
	removed.RemovedAt = &removedAt
	busd := &directoryBusd{reply: enroll.DirectoryReply{Directory: enroll.Directory{
		Records:  []bus.AgentRecord{removed, record("new@x")},
		Channels: map[string][]string{"team": {"new@x"}},
	}}}

	if err := mergeDirectory(context.Background(), busd, dir); err != nil {
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
}

func TestADirectoryAnswerThatCannotBeTrustedWritesNothing(t *testing.T) {
	cases := map[string]*directoryBusd{
		"busd is unreachable":   {err: errors.New("no responders")},
		"busd reports an error": {reply: enroll.DirectoryReply{Error: "the registry cannot be read"}},
		"a record names a path": {reply: enroll.DirectoryReply{Directory: enroll.Directory{Records: []bus.AgentRecord{record("good@x"), record("../evil@x")}}}},
		"a reader is invalid":   {reply: enroll.DirectoryReply{Directory: enroll.Directory{Channels: map[string][]string{"team": {"not an id"}}}}},
		"a channel is invalid":  {reply: enroll.DirectoryReply{Directory: enroll.Directory{Channels: map[string][]string{"Bad Name": nil}}}},
	}
	for name, busd := range cases {
		t.Run(name, func(t *testing.T) {
			dir := trust.DirectoryPath(t.TempDir())
			if err := mergeDirectory(context.Background(), busd, dir); err == nil {
				t.Fatal("the merge succeeded")
			}
			if records, _ := dir.Records(context.Background()); len(records) != 0 {
				t.Errorf("records were written: %+v", records)
			}
		})
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
