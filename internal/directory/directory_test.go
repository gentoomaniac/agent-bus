package directory

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

func record(agentID, fingerprint string) bus.AgentRecord {
	return bus.AgentRecord{AgentID: agentID, PublicKey: "key-" + fingerprint, Fingerprint: fingerprint,
		RegisteredAt: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC), Keys: []bus.KeyEntry{{PublicKey: "key-" + fingerprint, Fingerprint: fingerprint}}}
}

func removed(agentID, fingerprint string) bus.AgentRecord {
	tombstone := record(agentID, fingerprint)
	at := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	tombstone.RemovedAt = &at
	tombstone.Keys[0].Revoked, tombstone.Keys[0].RevokedAt = true, &at
	return tombstone
}

func TestTheHashIsTheSameForTheSameContentInAnyOrder(t *testing.T) {
	one := Projection{
		Records:   []bus.AgentRecord{record("b@x", "fb"), record("a@x", "fa")},
		Catalogue: trust.Catalogue{"team": {"b@x", "a@x"}, "empty": nil},
	}
	other := Projection{
		Records:   []bus.AgentRecord{record("a@x", "fa"), record("b@x", "fb")},
		Catalogue: trust.Catalogue{"empty": {}, "team": {"a@x", "b@x"}},
	}
	if one.Hash() != other.Hash() {
		t.Error("the same directory in another order hashes differently")
	}
	other.Catalogue["team"] = []string{"a@x"}
	if one.Hash() == other.Hash() {
		t.Error("a changed readership hashes the same")
	}
}

func TestTheHashSurvivesTheTripThroughJSON(t *testing.T) {
	local, _ := time.LoadLocation("Europe/Berlin")
	withZone := record("a@x", "fa")
	withZone.RegisteredAt = time.Date(2026, 11, 25, 10, 0, 0, 123400000, local)
	projection := Projection{Records: []bus.AgentRecord{withZone, removed("gone@x", "fg")}, Catalogue: trust.Catalogue{"team": {"a@x"}}}
	data, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Projection
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != projection.Hash() {
		t.Error("an agent that decodes the directory computes another hash than busd")
	}
}

func TestValidateRefusesWhatWouldNameAFileOutsideTheDirectory(t *testing.T) {
	for name, projection := range map[string]Projection{
		"bad ID":     {Records: []bus.AgentRecord{record("../x@y", "f")}},
		"twice":      {Records: []bus.AgentRecord{record("a@x", "f1"), record("a@x", "f2")}},
		"bad group":  {Catalogue: trust.Catalogue{"../team": nil}},
		"bad reader": {Catalogue: trust.Catalogue{"team": {"../x@y"}}},
	} {
		if projection.Validate() == nil {
			t.Errorf("%s: validated", name)
		}
	}
	good := Projection{Records: []bus.AgentRecord{record("a@x", "f")}, Catalogue: trust.Catalogue{"team": {"a@x"}}}
	if err := good.Validate(); err != nil {
		t.Errorf("a good directory: %v", err)
	}
}

func TestChangesAreTheNetChangeBetweenTwoProjections(t *testing.T) {
	before := Projection{
		Records:   []bus.AgentRecord{record("stays@x", "f1"), record("leaves@x", "f2"), record("rekeys@x", "f3")},
		Catalogue: trust.Catalogue{"team": {"stays@x"}, "old": {}},
	}
	after := Projection{
		Records: []bus.AgentRecord{
			record("stays@x", "f1"), removed("leaves@x", "f2"), record("rekeys@x", "f4"),
			record("joins@x", "f5"), removed("came-and-went@x", "f6"),
		},
		Catalogue: trust.Catalogue{"team": {"stays@x", "joins@x"}, "new": {}},
	}

	got := Changes(before, after)

	want := []Change{
		{Kind: Joined, AgentID: "joins@x"}, {Kind: Removed, AgentID: "leaves@x"}, {Kind: Rekeyed, AgentID: "rekeys@x"},
		{Kind: Channels, Channel: "new"}, {Kind: Channels, Channel: "old"}, {Kind: Channels, Channel: "team"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes = %+v\nwant %+v", got, want)
	}
	if changes := Changes(after, after); len(changes) != 0 {
		t.Errorf("no change gave %+v", changes)
	}
}
