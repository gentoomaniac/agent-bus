package directory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

func newCA(t *testing.T) (*tlsinit.CA, []byte) {
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
	return ca, caPEM
}

func versionOf(epoch string, revision uint64, projection Projection) Version {
	return Version{Epoch: epoch, Revision: revision, StateHash: projection.Hash()}
}

// twoRevisions is a directory at revision 1 and the same at revision 2, after carol joined, bob
// was removed and the team channel got a reader.
func twoRevisions() (first, second Projection) {
	first = Projection{Records: []bus.AgentRecord{record("alice@x", "fa"), record("bob@x", "fb")}, Catalogue: trust.Catalogue{"team": {"alice@x"}}}
	second = Projection{
		Records:   []bus.AgentRecord{record("alice@x", "fa"), removed("bob@x", "fb"), record("carol@x", "fc")},
		Catalogue: trust.Catalogue{"team": {"alice@x", "carol@x"}},
	}
	return first, second
}

func TestASignedReplyVerifiesForItsRequestOnly(t *testing.T) {
	ca, caPEM := newCA(t)
	_, otherPEM := newCA(t)
	request := NewSyncRequest(Version{})
	signed, err := SignSyncReply(ca, SyncReply{RequestHash: RequestHash(request), Kind: Unchanged})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := VerifySyncReply(caPEM, signed, request); err != nil {
		t.Errorf("the reply to this request: %v", err)
	}
	if _, err := VerifySyncReply(caPEM, signed, NewSyncRequest(Version{})); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Errorf("the reply to another request: %v, want a replay", err)
	}
	if _, err := VerifySyncReply(otherPEM, signed, request); err == nil {
		t.Error("a reply verified against another CA")
	}

	var tampered Signed
	_ = json.Unmarshal(signed, &tampered)
	tampered.Payload = []byte(strings.Replace(string(tampered.Payload), Unchanged, Full, 1))
	forged, _ := json.Marshal(tampered)
	if _, err := VerifySyncReply(caPEM, forged, request); err == nil {
		t.Error("a changed payload verified")
	}
	// The same payload signed for another purpose of the CA key is not a sync reply.
	otherDomain, _ := SignAsBusd(ca, "agent-bus/something-else/v1\n", SyncReply{RequestHash: RequestHash(request), Kind: Unchanged})
	if _, err := VerifySyncReply(caPEM, otherDomain, request); err == nil {
		t.Error("a signature of another domain verified")
	}
	if _, err := VerifySyncReply(caPEM, Unsigned("too busy"), request); err == nil || !strings.Contains(err.Error(), "too busy") {
		t.Errorf("an unsigned answer: %v, want it refused with its reason", err)
	}
}

func TestASignedErrorIsAnError(t *testing.T) {
	ca, caPEM := newCA(t)
	request := NewSyncRequest(Version{})
	signed, _ := SignSyncReply(ca, SyncReply{RequestHash: RequestHash(request), Kind: Failed, Error: "not built yet"})
	if _, err := VerifySyncReply(caPEM, signed, request); err == nil || !strings.Contains(err.Error(), "not built yet") {
		t.Errorf("a signed error: %v", err)
	}
}

func TestADiffAppliedOntoItsBaseGivesTheSignedState(t *testing.T) {
	first, second := twoRevisions()
	have, current := versionOf("e1", 1, first), versionOf("e1", 2, second)

	reply := Answer(have, current, second, &first)

	if reply.Kind != Diff || len(reply.Records) != 2 || reply.Catalogue == nil {
		t.Fatalf("answer = %s with %d records, catalogue %v; want a diff of bob and carol with the catalogue", reply.Kind, len(reply.Records), reply.Catalogue)
	}
	got, err := Apply(have, first, reply)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash() != second.Hash() {
		t.Error("the diff did not reproduce the new directory")
	}
}

func TestBusdAnswersUnchangedOrFull(t *testing.T) {
	first, second := twoRevisions()
	current := versionOf("e1", 2, second)

	if reply := Answer(current, current, second, nil); reply.Kind != Unchanged || len(reply.Records) != 0 {
		t.Errorf("an agent at the current version got %s with %d records", reply.Kind, len(reply.Records))
	}
	for name, since := range map[string]Version{
		"nothing":            {},
		"an unknown base":    versionOf("e1", 1, first),
		"another epoch":      versionOf("e0", 7, first),
		"a revision too far": {Epoch: "e1", Revision: 3, StateHash: "x"},
	} {
		reply := Answer(since, current, second, nil)
		if reply.Kind != Full || len(reply.Records) != 3 || reply.Catalogue == nil {
			t.Errorf("%s: %s with %d records", name, reply.Kind, len(reply.Records))
		}
		if _, err := Apply(since, first, reply); err != nil && name != "a revision too far" {
			t.Errorf("%s: applying the full directory: %v", name, err)
		}
	}
}

func TestApplyRefusesWhatDoesNotFit(t *testing.T) {
	first, second := twoRevisions()
	have, current := versionOf("e1", 1, first), versionOf("e1", 2, second)
	diff := Answer(have, current, second, &first)
	full := Answer(Version{}, current, second, nil)

	tamperedRecord := diff
	tamperedRecord.Records = append([]bus.AgentRecord{record("mallory@x", "fm")}, diff.Records...)
	wrongCatalogue := full
	wrongCatalogue.Catalogue = &trust.Catalogue{}
	noCatalogue := full
	noCatalogue.Catalogue = nil
	badID := full
	badID.Records = []bus.AgentRecord{record("../x@y", "f")}
	backwards := diff
	backwards.To = 1

	for name, test := range map[string]struct {
		have  Version
		reply SyncReply
		want  string
	}{
		"a diff onto another base":     {Version{Epoch: "e1", Revision: 1, StateHash: "other"}, diff, "diff"},
		"a diff from another epoch":    {versionOf("e0", 1, first), diff, "diff"},
		"a diff that goes back":        {have, backwards, "back"},
		"an added record":              {have, tamperedRecord, "hashes"},
		"another catalogue":            {Version{}, wrongCatalogue, "hashes"},
		"a full without a catalogue":   {Version{}, noCatalogue, "catalogue"},
		"an invalid ID":                {Version{}, badID, "invalid agent ID"},
		"a rollback in the same epoch": {versionOf("e1", 5, first), full, "rollback"},
		"unchanged, but not for this":  {have, Answer(current, current, second, nil), "unchanged"},
	} {
		if _, err := Apply(test.have, first, test.reply); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v, want an error about %q", name, err, test.want)
		}
	}
}

func TestANewEpochIsTakenAsAFullDirectory(t *testing.T) {
	first, second := twoRevisions()
	reply := Answer(versionOf("e1", 9, first), versionOf("e2", 1, second), second, nil)

	got, err := Apply(versionOf("e1", 9, first), first, reply)

	if err != nil || got.Hash() != second.Hash() {
		t.Errorf("a full directory of a new epoch, at a lower revision: %v", err)
	}
}

func TestParseSyncRequestTakesOnlyARequest(t *testing.T) {
	if _, err := ParseSyncRequest(NewSyncRequest(Version{Epoch: "e", Revision: 3, StateHash: "h"})); err != nil {
		t.Errorf("a request: %v", err)
	}
	for name, data := range map[string]string{
		"not JSON":       "hello",
		"no challenge":   `{"since":{"epoch":"","revision":0,"state_hash":""}}`,
		"unknown fields": `{"challenge":"c","since":{},"as":"admin"}`,
		"too long":       `{"challenge":"` + strings.Repeat("c", MaxSyncRequestBytes) + `"}`,
	} {
		if _, err := ParseSyncRequest([]byte(data)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestAnswerNormalisesWhatItIsGiven(t *testing.T) {
	first, second := twoRevisions()
	unsorted := Projection{Records: []bus.AgentRecord{second.Records[2], second.Records[0], second.Records[1]}, Catalogue: trust.Catalogue{"team": {"carol@x", "alice@x"}, "empty": nil}}
	base := Projection{Records: []bus.AgentRecord{first.Records[1], first.Records[0]}, Catalogue: first.Catalogue}
	have, current := versionOf("e1", 1, first), versionOf("e1", 2, unsorted)

	for _, reply := range []SyncReply{Answer(have, current, unsorted, &base), Answer(Version{}, current, unsorted, nil)} {
		if _, err := Apply(have, first, reply); err != nil {
			t.Errorf("%s of an unsorted projection: %v", reply.Kind, err)
		}
		if reply.Records[0].AgentID > reply.Records[len(reply.Records)-1].AgentID {
			t.Errorf("%s records are not sorted", reply.Kind)
		}
	}
}
