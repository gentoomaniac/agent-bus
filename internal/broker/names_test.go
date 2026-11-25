package broker

import (
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func TestSubjects(t *testing.T) {
	for channel, want := range map[bus.Channel]string{
		bus.GeneralChannel():         "general",
		bus.GroupChannel("release"):  "topic.release",
		bus.DMChannel("lib-x@bob"):   "dm.lib-x_bob",
		bus.DMChannel("human@alice"): "dm.human_alice",
	} {
		if got := subject(channel); got != want {
			t.Errorf("subject(%v) = %q, want %q", channel, got, want)
		}
	}
}

func TestConsumerNamesSplitAtDoubleUnderscore(t *testing.T) {
	const token = "lib-x_bob"
	for channel, want := range map[bus.Channel]string{
		bus.DMChannel("lib-x@bob"):  "lib-x_bob__dm",
		bus.GeneralChannel():        "lib-x_bob__general",
		bus.GroupChannel("release"): "lib-x_bob__g__release",
	} {
		got := consumerName(token, channel)
		if got != want {
			t.Errorf("consumerName(%v) = %q, want %q", channel, got, want)
		}
		if parts := strings.Split(got, "__"); parts[0] != token {
			t.Errorf("%q does not start with the agent token before the first __", got)
		}
	}
}

func TestReadCursorHolder(t *testing.T) {
	for _, test := range []struct {
		name    string
		channel bus.Channel
		want    string
	}{
		{"lib-x_bob__g__release", bus.GroupChannel("release"), "lib-x_bob"},
		{"lib-x_bob__g__release", bus.GroupChannel("rel"), ""},
		{"lib-x_bob__g__release", bus.GeneralChannel(), ""},
		{"lib-x_bob__general", bus.GeneralChannel(), "lib-x_bob"},
		{"lib-x_bob__dm", bus.DMChannel("lib-x@bob"), "lib-x_bob"},
		{"no-separator", bus.GeneralChannel(), ""},
	} {
		got, ok := readCursorHolder(test.name, test.channel)
		if got != test.want || ok != (test.want != "") {
			t.Errorf("readCursorHolder(%q, %v) = %q, %v; want %q", test.name, test.channel, got, ok, test.want)
		}
	}
}

func TestGroupCursorHolder(t *testing.T) {
	for name, want := range map[string][2]string{
		"lib-x_bob__g__release": {"lib-x_bob", "release"},
		"lib-x_bob__general":    {},
		"lib-x_bob__dm":         {},
		"a__b__g__x":            {}, // no agent token has "__"
		"monitor-ordered-123":   {},
	} {
		token, group, ok := groupCursorHolder(name)
		if [2]string{token, group} != want || ok != (want[0] != "") {
			t.Errorf("groupCursorHolder(%q) = %q, %q, %v; want %v", name, token, group, ok, want)
		}
	}
}
