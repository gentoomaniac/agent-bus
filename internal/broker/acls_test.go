package broker

import (
	"slices"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
)

// allows reports whether the rules give the operation on the topic, as Kafka would match them.
func allows(rules []ACLRule, topic string, operation kadm.ACLOperation) bool {
	for _, rule := range rules {
		matches := rule.Name == topic || (rule.Prefixed && strings.HasPrefix(topic, rule.Name))
		if !rule.Group && !rule.Cluster && matches && slices.Contains(rule.Operations, operation) {
			return true
		}
	}
	return false
}

func TestAgentCanReadOnlyItsOwnDMTopic(t *testing.T) {
	rules := AgentACLRules("lib-x@bob")
	if !allows(rules, "agents.dm.lib-x.bob", kadm.OpRead) {
		t.Error("agent cannot read its own DM topic")
	}
	if allows(rules, "agents.dm.api-client.alice", kadm.OpRead) {
		t.Error("agent can read another agent's DM topic")
	}
}

func TestAgentCanWriteAnyDMTopic(t *testing.T) {
	if !allows(AgentACLRules("lib-x@bob"), "agents.dm.api-client.alice", kadm.OpWrite) {
		t.Error("agent cannot write to another agent's DM topic")
	}
}

func TestAgentCanUseAnyGroupTopic(t *testing.T) {
	rules := AgentACLRules("lib-x@bob")
	for _, operation := range []kadm.ACLOperation{kadm.OpRead, kadm.OpWrite, kadm.OpDescribe} {
		if !allows(rules, "agents.topic.lib-x", operation) {
			t.Errorf("agent lacks %v on a group topic", operation)
		}
	}
}

func TestAgentCannotWriteRegistryOrCatalogue(t *testing.T) {
	rules := AgentACLRules("lib-x@bob")
	for _, topic := range []string{"agents.registry", "agents.topics"} {
		if allows(rules, topic, kadm.OpWrite) {
			t.Errorf("agent can write %s", topic)
		}
		if !allows(rules, topic, kadm.OpRead) {
			t.Errorf("agent cannot read %s", topic)
		}
	}
}

func TestAgentCanWriteIdempotently(t *testing.T) {
	for _, rule := range AgentACLRules("lib-x@bob") {
		if rule.Cluster && slices.Contains(rule.Operations, kadm.OpIdempotentWrite) {
			return
		}
	}
	t.Error("agent lacks IdempotentWrite on the cluster")
}

func TestAgentConsumerGroupIsItsOwnID(t *testing.T) {
	for _, rule := range AgentACLRules("lib-x@bob") {
		if rule.Group && (rule.Name != "lib-x@bob" || rule.Prefixed) {
			t.Errorf("group rule %+v is not the exact group lib-x@bob", rule)
		}
	}
}

func TestEveryRuleBuildsValidACLs(t *testing.T) {
	for _, rule := range AgentACLRules("lib-x@bob") {
		if err := rule.builder("User:lib-x@bob").ValidateCreate(); err != nil {
			t.Errorf("rule %+v: %v", rule, err)
		}
	}
}
