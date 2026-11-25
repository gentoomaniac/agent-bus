package bus

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAgentRecordKeyHistoryJSON(t *testing.T) {
	revokedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := AgentRecord{
		AgentID: "a@b",
		Keys: []KeyEntry{
			{PublicKey: "k1", Fingerprint: "fp1", Revoked: true, RevokedAt: &revokedAt},
			{PublicKey: "k2", Fingerprint: "fp2"},
		},
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"public_key", "fingerprint", "valid_from", "rotation_sig", "revoked"} {
		for i, key := range fields.Keys {
			if _, ok := key[name]; !ok {
				t.Errorf("key %d has no %q field", i, name)
			}
		}
	}
	if _, ok := fields.Keys[0]["revoked_at"]; !ok {
		t.Error("a revoked key has no revoked_at")
	}
	if _, ok := fields.Keys[1]["revoked_at"]; ok {
		t.Error("a key that is not revoked has revoked_at")
	}

	var decoded AgentRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Keys) != 2 || !decoded.Keys[0].RevokedAt.Equal(revokedAt) || decoded.Keys[1].RevokedAt != nil {
		t.Errorf("round trip changed the history: %+v", decoded.Keys)
	}
}

// A catalogue entry written before channels had owners is the bus owner's channel.
func TestTopicRecordOfAnOlderReleaseHasNoOwner(t *testing.T) {
	var record TopicRecord
	if err := json.Unmarshal([]byte(`{"name":"lib-x","description":"about lib-x","updated_at":"2026-11-01T10:00:00Z"}`), &record); err != nil {
		t.Fatal(err)
	}
	if record.Name != "lib-x" || record.Owner != "" || !record.CreatedAt.IsZero() {
		t.Errorf("record = %+v", record)
	}
	encoded, err := json.Marshal(TopicRecord{Name: "lib-x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"lib-x","description":"","updated_at":"0001-01-01T00:00:00Z"}`; string(encoded) != want {
		t.Errorf("an entry of the bus owner is %s, want %s", encoded, want)
	}
}
