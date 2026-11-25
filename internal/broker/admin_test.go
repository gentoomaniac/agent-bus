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

func TestCallerOf(t *testing.T) {
	const prefix = "busd.directory.sync"
	for _, test := range []struct {
		subject, reply string
		caller         string
		allowed        bool
	}{
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob.abc", "lib-x@bob", true},
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob.abc.1", "lib-x@bob", true},
		{prefix + ".lib-x_bob", "_INBOX.other_bob.abc", "", false}, // another agent's inbox
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob", "", false},     // no token below the prefix
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob.*", "", false},
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob.>", "", false},
		{prefix + ".lib-x_bob", "_INBOX.lib-x_bob..x", "", false},
		{prefix + ".lib-x_bob", "dm.lib-x_bob", "", false},
		{prefix + ".lib-x_bob", "$JS.API.STREAM.DELETE.BUS", "", false},
		{prefix + ".enroll", "_INBOX.enroll.abc", "", false}, // not an agent token
		{prefix + ".Lib_bob", "_INBOX.Lib_bob.abc", "", false},
		{prefix, "_INBOX.lib-x_bob.abc", "", false},
		{"busd.other.lib-x_bob", "_INBOX.lib-x_bob.abc", "", false},
	} {
		caller, allowed := callerOf(prefix, test.subject, test.reply)
		if allowed != test.allowed || allowed && caller != test.caller {
			t.Errorf("callerOf(%q, %q) = %q, %v; want %q, %v", test.subject, test.reply, caller, allowed, test.caller, test.allowed)
		}
	}
}
