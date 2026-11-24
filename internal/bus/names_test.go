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

func TestValidateGroupName(t *testing.T) {
	for _, name := range []string{"lib-x", "release", "v2"} {
		if err := ValidateGroupName(name); err != nil {
			t.Errorf("ValidateGroupName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "Lib", "a.b", "a_b", "a@b", "a b"} {
		if ValidateGroupName(name) == nil {
			t.Errorf("ValidateGroupName(%q) = nil, want error", name)
		}
	}
}

func TestAgentToken(t *testing.T) {
	for agentID, want := range map[string]string{"lib-x@bob": "lib-x_bob", "human@alice": "human_alice"} {
		if got := AgentToken(agentID); got != want {
			t.Errorf("AgentToken(%q) = %q, want %q", agentID, got, want)
		}
	}
}
