package trust

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

const lib = "lib-x@bob"

type fakeDirectory map[string]bus.AgentRecord

func (directory fakeDirectory) Lookup(_ context.Context, agentID string) (bus.AgentRecord, error) {
	record, ok := directory[agentID]
	if !ok {
		return record, errors.New("no such file")
	}
	return record, nil
}

func entryFor(public ed25519.PublicKey) bus.KeyEntry {
	return bus.KeyEntry{PublicKey: base64.StdEncoding.EncodeToString(public), Fingerprint: identity.Fingerprint(public)}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "known_agents"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFirstContactPinsTheRegistryKeyAndLaterMessagesUseThePin(t *testing.T) {
	public, _ := identity.Generate()
	entry := entryFor(public)
	directory := fakeDirectory{lib: {AgentID: lib, Keys: []bus.KeyEntry{entry}}}
	store := openStore(t)

	resolution, err := store.Resolve(context.Background(), directory, lib, entry.Fingerprint, time.Now())
	if err != nil || resolution.NewPin == nil || !resolution.Key.Equal(public) {
		t.Fatalf("first contact: %+v, %v", resolution, err)
	}
	if len(store.Pins(lib)) != 0 {
		t.Error("Resolve pinned the key before the signature was checked")
	}
	if err := store.Add(*resolution.NewPin); err != nil {
		t.Fatal(err)
	}

	// The second contact needs no registry, and the pin survives a reopen.
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err = reopened.Resolve(context.Background(), fakeDirectory{}, lib, entry.Fingerprint, time.Now())
	if err != nil || resolution.NewPin != nil || resolution.Source != SourceTOFU || !resolution.Key.Equal(public) {
		t.Errorf("second contact: %+v, %v", resolution, err)
	}
}

func TestChangedKeyIsRefused(t *testing.T) {
	oldPublic, _ := identity.Generate()
	newPublic, _ := identity.Generate()
	store := openStore(t)
	old := entryFor(oldPublic)
	if err := store.Add(Pin{AgentID: lib, Fingerprint: old.Fingerprint, PublicKey: old.PublicKey, FirstSeen: time.Now(), Source: SourcePinned}); err != nil {
		t.Fatal(err)
	}
	// Even a registry that lists the new key does not make it acceptable: no rotation chains yet.
	directory := fakeDirectory{lib: {AgentID: lib, Keys: []bus.KeyEntry{old, entryFor(newPublic)}}}
	_, err := store.Resolve(context.Background(), directory, lib, identity.Fingerprint(newPublic), time.Now())
	var changed ChangedKeyError
	if !errors.As(err, &changed) || changed.PinnedFingerprint != old.Fingerprint {
		t.Errorf("err = %v, want ChangedKeyError naming the pinned key", err)
	}
}

func TestRegistryProblems(t *testing.T) {
	public, _ := identity.Generate()
	entry := entryFor(public)
	revoked := entry
	revoked.Revoked = true
	mismatched := entry
	mismatched.Fingerprint = "SHA256:forged"
	otherPublic, _ := identity.Generate()

	cases := map[string]struct {
		directory fakeDirectory
		fp        string
		want      string
		retryable bool
	}{
		"record missing":        {directory: fakeDirectory{}, fp: entry.Fingerprint, retryable: true},
		"key not in the record": {directory: fakeDirectory{lib: {Keys: []bus.KeyEntry{entry}}}, fp: identity.Fingerprint(otherPublic), want: "not in the registry"},
		"revoked key":           {directory: fakeDirectory{lib: {Keys: []bus.KeyEntry{revoked}}}, fp: entry.Fingerprint, want: "revoked"},
		"fingerprint forged":    {directory: fakeDirectory{lib: {Keys: []bus.KeyEntry{mismatched}}}, fp: "SHA256:forged", want: "does not match"},
	}
	for name, test := range cases {
		_, err := openStore(t).Resolve(context.Background(), test.directory, lib, test.fp, time.Now())
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if errors.Is(err, ErrUnavailable) != test.retryable {
			t.Errorf("%s: retryable = %v for %v", name, !test.retryable, err)
		} else if test.want != "" && !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error %q lacks %q", name, err, test.want)
		}
	}
}

func TestRecordWithoutKeysListIsOneKey(t *testing.T) {
	public, _ := identity.Generate()
	entry := entryFor(public)
	directory := fakeDirectory{lib: {AgentID: lib, PublicKey: entry.PublicKey, Fingerprint: entry.Fingerprint}}
	resolution, err := openStore(t).Resolve(context.Background(), directory, lib, entry.Fingerprint, time.Now())
	if err != nil || !resolution.Key.Equal(public) {
		t.Errorf("%+v, %v", resolution, err)
	}
}

func TestFileDirectoryRoundTrip(t *testing.T) {
	directory := DirectoryPath(t.TempDir())
	public, _ := identity.Generate()
	record := bus.AgentRecord{AgentID: lib, PublicKey: entryFor(public).PublicKey, Keys: []bus.KeyEntry{entryFor(public)}}
	if _, err := directory.Lookup(context.Background(), lib); err == nil {
		t.Error("a missing record was found")
	}
	if err := directory.Write(record); err != nil {
		t.Fatal(err)
	}
	got, err := directory.Lookup(context.Background(), lib)
	if err != nil || got.AgentID != lib || len(got.Keys) != 1 || got.Keys[0].Fingerprint != identity.Fingerprint(public) {
		t.Errorf("Lookup = %+v, %v", got, err)
	}
}
