package bus

import "testing"

func TestValidateAgentID(t *testing.T) {
	valid := []string{"lib-x@bob", "api-client@alice", "a1@b2", "human@alice"}
	invalid := []string{"", "lib-x", "@bob", "lib-x@", "Lib-x@bob", "lib_x@bob", "a@b@c", "a.b@bob", "a b@bob", "lib-x@bob\n"}
	for _, id := range valid {
		if err := ValidateAgentID(id); err != nil {
			t.Errorf("ValidateAgentID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range invalid {
		if ValidateAgentID(id) == nil {
			t.Errorf("ValidateAgentID(%q) = nil, want error", id)
		}
	}
}

func TestValidateTopicName(t *testing.T) {
	for _, name := range []string{"lib-x", "release", "v2"} {
		if err := ValidateTopicName(name); err != nil {
			t.Errorf("ValidateTopicName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "Lib", "a.b", "a_b", "a@b", "a b"} {
		if ValidateTopicName(name) == nil {
			t.Errorf("ValidateTopicName(%q) = nil, want error", name)
		}
	}
}

func TestTopicMapping(t *testing.T) {
	if got, want := DMTopic("lib-x@bob"), "agents.dm.lib-x.bob"; got != want {
		t.Errorf("DMTopic = %q, want %q", got, want)
	}
	if got, want := GroupTopic("release"), "agents.topic.release"; got != want {
		t.Errorf("GroupTopic = %q, want %q", got, want)
	}
}
