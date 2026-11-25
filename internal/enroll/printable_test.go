package enroll

import "testing"

func TestPrintableDropsWhatMovesATerminal(t *testing.T) {
	for input, want := range map[string]string{
		"plain text, ünïcode ✓":            "plain text, ünïcode ✓",
		"\x1b[2J\x1b[Hfake screen":         "[2J[Hfake screen", // the escape is gone, so it is only text
		"line one\nline two\r":             "line oneline two",
		"bell\a and \x00 nul and \x7f del": "bell and  nul and  del",
		"\u202eevil\u202c":                 "evil", // bidirectional overrides
		"c1 control \u009b31m":             "c1 control 31m",
		"":                                 "",
	} {
		if got := Printable(input); got != want {
			t.Errorf("Printable(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPendingPrintableKeepsTheRequestID(t *testing.T) {
	pending := Pending{RequestID: "abc123", Code: "AB\x1b-CD", AgentID: "a\n@b", Hostname: "host\x1b[31m", Identity: "unknown: \x1b[2J", SigningFingerprint: "SHA256:x\r", TLSFingerprint: "y\a"}
	clean := pending.Printable()
	if clean.RequestID != "abc123" {
		t.Errorf("the request ID changed: %q", clean.RequestID)
	}
	for _, field := range []string{clean.Code, clean.AgentID, clean.Hostname, clean.Identity, clean.SigningFingerprint, clean.TLSFingerprint} {
		if field != Printable(field) {
			t.Errorf("%q still has unprintable characters", field)
		}
	}
	if clean.Hostname != "host[31m" {
		t.Errorf("hostname %q", clean.Hostname)
	}
}
