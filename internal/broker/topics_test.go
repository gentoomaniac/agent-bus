package broker

import "testing"

func TestTopicConfigs(t *testing.T) {
	compacted := Topic{Name: "t", Compacted: true}.configs()
	if got := *compacted["cleanup.policy"]; got != "compact" {
		t.Errorf("compacted cleanup.policy = %q, want compact", got)
	}
	infinite := Topic{Name: "t"}.configs()
	for name, want := range map[string]string{"cleanup.policy": "delete", "retention.ms": "-1", "retention.bytes": "-1"} {
		if got := *infinite[name]; got != want {
			t.Errorf("infinite topic %s = %q, want %q", name, got, want)
		}
	}
}

func TestSystemTopicsFollowTheDesignTable(t *testing.T) {
	compacted := map[string]bool{}
	for _, topic := range SystemTopics {
		compacted[topic.Name] = topic.Compacted
	}
	want := map[string]bool{
		"agents.registry": true,
		"agents.general":  false,
		"agents.requests": true,
		"agents.topics":   true,
	}
	if len(compacted) != len(want) {
		t.Fatalf("system topics = %v, want %v", compacted, want)
	}
	for name, isCompacted := range want {
		if got, ok := compacted[name]; !ok || got != isCompacted {
			t.Errorf("%s: compacted = %v (present %v), want %v", name, got, ok, isCompacted)
		}
	}
}
