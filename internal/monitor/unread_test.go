package monitor

import (
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

func unreadFixture() *index {
	index := newIndex()
	for _, message := range []Message{
		{Sequence: 1, Channel: "general", Status: Verified, From: "bob@x", Targets: []string{"@all"}},
		{Sequence: 2, Channel: "general", Status: Verified, From: "me@home", Targets: []string{"@all"}}, // my own
		{Sequence: 3, Channel: "general", Status: Verified, From: "bob@x", Targets: []string{"carol@x"}},
		{Sequence: 4, Channel: "general", Status: Verified, From: "carol@x", Targets: []string{"bob@x", "me@home"}},
		{Sequence: 5, Channel: "general", Status: Rejected, Reason: "not json"},                         // no sender, not mail for me
		{Sequence: 6, Channel: "group:ops", Status: Verified, From: "bob@x", Targets: []string{"@all"}}, // another channel
	} {
		index.add(message)
	}
	return index
}

func TestUnreadCountsWhatFollowsTheMarkerAndIsNotMine(t *testing.T) {
	index := unreadFixture()
	for _, test := range []struct {
		read             uint64
		unread, targeted int
	}{
		{0, 4, 2},  // 1, 3, 4 and 5, a record with no sender that no agent wrote; targeted: 1 (everyone) and 4
		{1, 3, 1},  // 3, 4, 5
		{3, 2, 1},  // 4, 5
		{4, 1, 0},  // 5
		{99, 0, 0}, // beyond the last
	} {
		if unread, targeted := index.unread("general", test.read, "me@home"); unread != test.unread || targeted != test.targeted {
			t.Errorf("read through %d: %d unread, %d targeted, want %d and %d", test.read, unread, targeted, test.unread, test.targeted)
		}
	}
	if unread, targeted := index.unread("nowhere", 0, "me@home"); unread != 0 || targeted != 0 {
		t.Errorf("an unknown channel: %d, %d", unread, targeted)
	}
	// A monitor without an identity wrote nothing, and only @all is aimed at it. A record with no
	// sender then counts as its own, as on the clients.
	if unread, targeted := index.unread("general", 0, ""); unread != 4 || targeted != 2 {
		t.Errorf("no agent: %d unread, %d targeted, want 4 and 2", unread, targeted)
	}
}

func TestChannelsCarryTheCountsOfTheSharedReadMarkers(t *testing.T) {
	monitor := New(Config{})
	monitor.index = unreadFixture()
	monitor.groups = []bus.TopicRecord{{Name: "ops"}}

	counts := func() map[string][2]int {
		result := map[string][2]int{}
		for _, channel := range monitor.Channels() {
			result[channel.ID] = [2]int{channel.Unread, channel.TargetedUnread}
		}
		return result
	}
	if got := counts(); got["general"] != [2]int{} || got["group:ops"] != [2]int{} {
		t.Errorf("before any read marker everything counts as seen, got %v", got)
	}

	if _, err := monitor.prefs.mergeRead(map[string]uint64{"general": 1}); err != nil {
		t.Fatal(err)
	}
	// "group:ops" has no marker while another channel has one: it is a channel that appeared later,
	// so all of it is unread. The monitor has no agent here, so @all is all that is aimed at it.
	if got := counts(); got["general"] != [2]int{3, 1} || got["group:ops"] != [2]int{1, 1} {
		t.Errorf("counts = %v, want general {3, 1} and group:ops {1, 1}", got)
	}
}
