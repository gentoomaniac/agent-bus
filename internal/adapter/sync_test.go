package adapter

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// copyingBusd is the directory fetch of an agent whose own directory is empty: it copies the
// records of the registry directory, which stands in for busd's registry.
func (b *testBus) copyingBusd(into trust.FileDirectory, fetches *int) func(context.Context) error {
	return func(ctx context.Context) error {
		*fetches++
		records, err := trust.DirectoryPath(b.configDir).Records(ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := into.Write(record); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestSyncDirectoryFetchesNewRecordsAndAppliesTheirRevocations(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	aliceAdapter, _ := b.open(alice)
	b.remove(alice)
	own := trust.DirectoryPath(t.TempDir())
	fetches := 0
	bobAdapter, _ := b.open(bob, func(config *Config) {
		config.Directory, config.FetchDirectory = own, b.copyingBusd(own, &fetches)
	})
	ctx := context.Background()

	if err := bobAdapter.SyncDirectory(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := own.Lookup(ctx, alice); err != nil {
		t.Errorf("alice's record was not fetched: %v", err)
	}
	reopened, err := trust.Open(filepath.Join(b.configDir, bob, knownAgentsFile))
	if err != nil {
		t.Fatal(err)
	}
	// Against an empty directory, so that only the remembered revocation can refuse the key.
	if _, err := reopened.Resolve(ctx, trust.DirectoryPath(t.TempDir()), alice, aliceAdapter.Fingerprint(), b.now); err == nil || !strings.Contains(err.Error(), "revoked key") {
		t.Error("alice's revoked key was not recorded")
	}
}

func TestSyncDirectoryKeepsTheLocalDirectoryWhenTheFetchFails(t *testing.T) {
	b := newTestBus(t)
	b.register(alice)
	b.register(bob)
	b.remove(alice)
	bobAdapter, _ := b.open(bob, func(config *Config) {
		config.FetchDirectory = func(context.Context) error { return errors.New("busd is down") }
	})

	err := bobAdapter.SyncDirectory(context.Background())

	if err == nil || err.Error() != "busd is down" {
		t.Errorf("SyncDirectory = %v, want the fetch error and nothing else", err)
	}
	if _, err := bobAdapter.Agents(context.Background()); err != nil {
		t.Errorf("the local directory stopped working: %v", err)
	}
}
