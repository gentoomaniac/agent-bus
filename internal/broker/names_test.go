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
