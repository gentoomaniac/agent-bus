package envelope

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/identity"
)

func TestCanonicalizeKeyOrderFollowsUTF16(t *testing.T) {
	// RFC 8785 section 3.2.3: the emoji (surrogates D83D DE00) sorts before U+FB33.
	input := `{"\u20ac":"Euro","\r":"CR","\ufb33":"Dalet","1":"One","\ud83d\ude00":"Emoji","\u0080":"Control","\u00f6":"o"}`
	want := "{\"\\r\":\"CR\",\"1\":\"One\",\"\u0080\":\"Control\",\"ö\":\"o\",\"€\":\"Euro\",\"\U0001F600\":\"Emoji\",\"\ufb33\":\"Dalet\"}"
	got, err := canonicalize([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestCanonicalizeStringsAndNumbers(t *testing.T) {
	// RFC 8785 section 3.2.2: whitespace dropped, escapes minimal, control characters as \u00xx.
	got, err := canonicalize([]byte("{ \"a\" : [ 1 , -0 , 9007199254740992 , \"\\u000f\\u007f/\\\"\" , true , null ] }"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":[1,0,9007199254740992,\"\\u000f\u007f/\\\"\",true,null]}"; string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestCanonicalizeRefusesWhatItCannotFormat(t *testing.T) {
	for _, input := range []string{`1.5`, `1e3`, `9007199254740993`, `{} {}`} {
		if _, err := canonicalize([]byte(input)); err == nil {
			t.Errorf("%s: want an error", input)
		}
	}
}

func testEnvelope(public ed25519.PublicKey) Envelope {
	return Envelope{
		Version: Version, ID: NewID(time.Now()), TS: time.Now().UTC().Truncate(time.Second),
		From: "a@o", To: To{Kind: ToAgent, Name: "b@o"}, Type: "message", Targets: []string{"c@o"}, Thread: "T", Hops: 1,
		KeyFP: identity.Fingerprint(public), Body: Body{Text: "héllo \"world\""},
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	public, private := identity.Generate()
	payload, err := Sign(testEnvelope(public), private)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !signed.Verify(public) {
		t.Error("signature of a fresh message does not verify")
	}
	otherPublic, _ := identity.Generate()
	if signed.Verify(otherPublic) {
		t.Error("signature verifies under another key")
	}
}

func TestAnyAlteredFieldFailsVerification(t *testing.T) {
	public, private := identity.Generate()
	payload, err := Sign(testEnvelope(public), private)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ old, new string }{
		{`"b@o"`, `"c@o"`}, {`"hops":1`, `"hops":0`}, {`"type":"message"`, `"type":"answer"`},
		{`"from":"a@o"`, `"from":"x@o"`}, {`"text":"h`, `"text":"H`}, {`"reply_to":null`, `"reply_to":"X"`},
		{`"targets":["c@o"]`, `"targets":["@all"]`},
	} {
		altered := strings.Replace(string(payload), change.old, change.new, 1)
		if altered == string(payload) {
			t.Fatalf("test bug: %q not found in %s", change.old, payload)
		}
		signed, err := Parse([]byte(altered))
		if err == nil && signed.Verify(public) {
			t.Errorf("change %s -> %s still verifies", change.old, change.new)
		}
	}
}

func TestUnknownFieldsAreCovered(t *testing.T) {
	public, private := identity.Generate()
	payload, _ := Sign(testEnvelope(public), private)
	altered := strings.Replace(string(payload), `{"v":1,`, `{"v":1,"extra":"x",`, 1)
	signed, err := Parse([]byte(altered))
	if err != nil {
		t.Fatal(err)
	}
	if signed.Verify(public) {
		t.Error("an added unknown field still verifies")
	}
}

func TestSizeLimits(t *testing.T) {
	public, private := identity.Generate()
	env := testEnvelope(public)
	env.Body.Text = strings.Repeat("x", MaxBodyBytes)
	if _, err := Sign(env, private); err == nil {
		t.Error("a body over the limit was signed")
	}
	if _, err := Parse(make([]byte, MaxRecordBytes+1)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("an oversized record was parsed: %v", err)
	}
}

func TestParseRejectsBadRecords(t *testing.T) {
	public, private := identity.Generate()
	env := testEnvelope(public)
	env.Version = 2
	payload, _ := Sign(env, private)
	if _, err := Parse(payload); err == nil {
		t.Error("an unsupported version was parsed")
	}
	if _, err := Parse([]byte(`{"envelope":{},"sig":"AAAA"}`)); err == nil {
		t.Error("a short signature was parsed")
	}
	if _, err := Parse([]byte(`nope`)); err == nil {
		t.Error("garbage was parsed")
	}
}

func TestCheckTo(t *testing.T) {
	env := Envelope{To: To{Kind: ToAgent, Name: "b@o"}, Targets: []string{"b@o"}}
	if err := env.CheckTo(bus.DMChannel("b@o")); err != nil {
		t.Error(err)
	}
	for _, targets := range [][]string{nil, {"c@o"}, {"b@o", "c@o"}, {TargetAll}} {
		if (Envelope{To: env.To, Targets: targets}).CheckTo(bus.DMChannel("b@o")) == nil {
			t.Errorf("a DM with targets %v was accepted", targets)
		}
	}
	for _, channel := range []bus.Channel{bus.DMChannel("c@o"), bus.GeneralChannel(), bus.GroupChannel("b@o")} {
		if env.CheckTo(channel) == nil {
			t.Errorf("accepted on %s", channel)
		}
	}
	general := Envelope{To: To{Kind: ToGroup, Name: "general"}, Targets: []string{TargetAll}}
	if err := general.CheckTo(bus.GeneralChannel()); err != nil {
		t.Error(err)
	}
	if general.CheckTo(bus.GroupChannel("lib-x")) == nil {
		t.Error("a general post was accepted on a group channel")
	}
}

func TestCheckNotFuture(t *testing.T) {
	now := time.Now()
	if err := (Envelope{TS: now.Add(4 * time.Minute)}).CheckNotFuture(now); err != nil {
		t.Error(err)
	}
	if (Envelope{TS: now.Add(6 * time.Minute)}).CheckNotFuture(now) == nil {
		t.Error("a timestamp 6 minutes ahead was accepted")
	}
	if err := (Envelope{TS: now.Add(-72 * time.Hour)}).CheckNotFuture(now); err != nil {
		t.Errorf("old timestamps must pass: %v", err)
	}
}

func TestNewIDIsAULIDThatSortsByTime(t *testing.T) {
	earlier, later := NewID(time.UnixMilli(1_700_000_000_000)), NewID(time.UnixMilli(1_700_000_000_001))
	if len(earlier) != 26 || earlier >= later {
		t.Errorf("IDs %s and %s are not 26 characters in time order", earlier, later)
	}
	if first := NewID(time.Now())[0]; first > '7' {
		t.Errorf("first character %c overflows 128 bits", first)
	}
}

func TestSignedRecordIsPlainJSON(t *testing.T) {
	public, private := identity.Generate()
	payload, _ := Sign(testEnvelope(public), private)
	var shape struct {
		Envelope map[string]any `json:"envelope"`
		Sig      string         `json:"sig"`
	}
	if err := json.Unmarshal(payload, &shape); err != nil || shape.Sig == "" || shape.Envelope["from"] != "a@o" {
		t.Errorf("unexpected record %s (%v)", payload, err)
	}
}

func TestDuplicateKeysAreRefused(t *testing.T) {
	public, private := identity.Generate()
	payload, err := Sign(testEnvelope(public), private)
	if err != nil {
		t.Fatal(err)
	}
	altered := strings.Replace(string(payload), `"body":`, `"body":{"title":"unsigned"},"body":`, 1)
	if _, err := Parse([]byte(altered)); err == nil {
		t.Fatal("a record with a duplicate key was accepted")
	}
}

func TestInvalidUnicodeIsRefused(t *testing.T) {
	public, private := identity.Generate()
	payload, err := Sign(testEnvelope(public), private)
	if err != nil {
		t.Fatal(err)
	}
	withText := func(text string) []byte {
		return []byte(strings.Replace(string(payload), `"body":{`, `"body":{"x":"`+text+`",`, 1))
	}
	// `\\ud800` is an escaped backslash followed by plain text, not a surrogate.
	for _, text := range []string{`😀`, `\\ud800`, "café"} {
		if _, err := Parse(withText(text)); err != nil {
			t.Errorf("%q: %v, want it accepted", text, err)
		}
	}
	for name, text := range map[string]string{
		"invalid UTF-8":      "a\xffb",
		"lone high":          `\ud83d`,
		"high before text":   `\ud83dx`,
		"high before high":   `\ud83d😀`,
		"stray low":          `\ude00`,
		"low before high":    `\ude00\ud83d`,
		"high before escape": `\ud83d\n`,
	} {
		if _, err := Parse(withText(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckNoDuplicateKeys(t *testing.T) {
	for _, ok := range []string{`{"a":{"x":1},"b":{"x":2}}`, `[{"a":1},{"a":2}]`, `{"a":[1,2,{"b":null}]}`} {
		if err := checkNoDuplicateKeys([]byte(ok)); err != nil {
			t.Errorf("%s: %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `[{"a":1,"a":1}]`, `{"body":1,"Body":2}`, `{"a":1} {}`} {
		if checkNoDuplicateKeys([]byte(bad)) == nil {
			t.Errorf("%s: nil, want error", bad)
		}
	}
}

func TestCheckTargets(t *testing.T) {
	for targets, want := range map[string]bool{
		`[]`: false, `["a@o"]`: true, `["a@o","b@o"]`: true, `["@all"]`: true,
		`["@all","a@o"]`: false, `["a@o","a@o"]`: false, `["nobody"]`: false, `["@all","@all"]`: false,
	} {
		var list []string
		if err := json.Unmarshal([]byte(targets), &list); err != nil {
			t.Fatal(err)
		}
		if err := (Envelope{Targets: list}).CheckTargets(); (err == nil) != want {
			t.Errorf("targets %s: valid = %v, want %v (%v)", targets, err == nil, want, err)
		}
	}
}

func TestSignRefusesInvalidAndMissingTargets(t *testing.T) {
	public, private := identity.Generate()
	env := testEnvelope(public)
	env.Targets = []string{"nobody"}
	if _, err := Sign(env, private); err == nil {
		t.Error("an invalid target was signed")
	}
	for _, none := range [][]string{nil, {}} {
		env.Targets = none
		if _, err := Sign(env, private); err == nil {
			t.Errorf("a message with targets %v was signed", none)
		}
	}
}

func TestParseRefusesInvalidTargets(t *testing.T) {
	_, private := identity.Generate()
	raw := `{"v":1,"id":"X","ts":"2026-11-24T10:00:00Z","from":"a@o","to":{"kind":"agent","name":"b@o"},"type":"message","targets":["@all","a@o"],"thread":"T","reply_to":null,"hops":0,"key_fp":"F","body":{}}`
	canonical, _ := canonicalize([]byte(raw))
	record, _ := json.Marshal(map[string]any{"envelope": json.RawMessage(raw), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))})
	if _, err := Parse(record); err == nil || !strings.Contains(err.Error(), "target") {
		t.Errorf("invalid targets were parsed: %v", err)
	}
}

func TestParseRefusesMissingAndEmptyTargets(t *testing.T) {
	_, private := identity.Generate()
	for _, field := range []string{``, `"targets":[],`, `"targets":null,`} {
		raw := `{"v":1,"id":"X","ts":"2026-11-24T10:00:00Z","from":"a@o","to":{"kind":"agent","name":"b@o"},"type":"message",` + field + `"thread":"T","reply_to":null,"hops":0,"key_fp":"F","body":{}}`
		canonical, _ := canonicalize([]byte(raw))
		record, _ := json.Marshal(map[string]any{"envelope": json.RawMessage(raw), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))})
		if _, err := Parse(record); err == nil || !strings.Contains(err.Error(), "targets") {
			t.Errorf("%q: a record without targets was parsed: %v", field, err)
		}
	}
}

func TestMentionedTargets(t *testing.T) {
	for text, want := range map[string][]string{
		"hello":                          nil,
		"@lib-x@bob can you do xyz?":     {"lib-x@bob"},
		"hi (@a@o), @b@o and @a@o again": {"a@o", "b@o"},
		"@b@o and @all":                  {"@all"},
		"mail me at x@o or @alloy":       nil,
		"line one\n@a@o line two":        {"a@o"},
	} {
		if got := MentionedTargets(text); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}
