package broker

import "testing"

func TestIsReplyBelow(t *testing.T) {
	const prefix = "_INBOX.enroll"
	for reply, want := range map[string]bool{
		"_INBOX.enroll.abc":            true,
		"_INBOX.enroll.abc.def":        true,
		"_INBOX.enroll":                false, // no token below the prefix
		"_INBOX.enroll.":               false,
		"_INBOX.enrollx.abc":           false, // another space that starts with the same letters
		"_INBOX.other.abc":             false,
		"_INBOX.enroll.*":              false, // a wildcard is not a literal subject
		"_INBOX.enroll.>":              false,
		"_INBOX.enroll.a..b":           false,
		"_INBOX.enroll.a b":            false,
		"$KV.registry.alice_bob":       false,
		"$JS.API.STREAM.DELETE.LEDGER": false,
		"busd.enroll.pending":          false,
		"":                             false,
	} {
		if got := isReplyBelow(reply, prefix); got != want {
			t.Errorf("isReplyBelow(%q, %q) = %v, want %v", reply, prefix, got, want)
		}
	}
}
