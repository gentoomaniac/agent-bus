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

func TestIsAgentInbox(t *testing.T) {
	for reply, want := range map[string]bool{
		"_INBOX.lib-x_bob.abc":      true,
		"_INBOX.lib-x_bob.abc.1":    true,
		"_INBOX.lib-x_bob":          false, // no token below the agent's prefix
		"_INBOX.lib-x_bob.*":        false,
		"_INBOX.lib-x_bob.>":        false,
		"_INBOX.lib-x_bob..x":       false,
		"_INBOX.lib-x_bob.a b":      false,
		"_INBOX.enroll.abc":         false, // not an agent token
		"_INBOX.bus-monitor.abc":    false,
		"_INBOX.Lib_bob.abc":        false,
		"_INBOX.*.abc":              false,
		"dm.lib-x_bob":              false,
		"directory.head":            false,
		"$KV.registry.lib-x_bob":    false,
		"$JS.API.STREAM.DELETE.BUS": false,
		"":                          false,
	} {
		if got := isAgentInbox(reply); got != want {
			t.Errorf("isAgentInbox(%q) = %v, want %v", reply, got, want)
		}
	}
}
