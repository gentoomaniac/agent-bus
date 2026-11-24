package trust

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

func TestRevokedFingerprintIsRefusedEvenWhenPinnedAndSurvivesReopen(t *testing.T) {
	public, _ := identity.Generate()
	entry := entryFor(public)
	store := openStore(t)
	if err := store.Add(Pin{AgentID: lib, Fingerprint: entry.Fingerprint, PublicKey: entry.PublicKey, FirstSeen: time.Now(), Source: SourcePinned}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), fakeDirectory{}, lib, entry.Fingerprint, time.Now()); err != nil {
		t.Fatalf("pinned key refused before the revocation: %v", err)
	}

	if err := store.Revoke(lib, entry.Fingerprint); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*Store{"same store": store, "reopened": reopened} {
		_, err := s.Resolve(context.Background(), fakeDirectory{}, lib, entry.Fingerprint, time.Now())
		if err == nil || !strings.Contains(err.Error(), "revoked key") {
			t.Errorf("%s: err = %v, want revoked key", name, err)
		}
	}
}
