package broker

import (
	"context"
	"fmt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"github.com/twmb/franz-go/pkg/kadm"
)

// ACLRule allows one principal some operations on a topic or a consumer group.
type ACLRule struct {
	Group      bool // a consumer group; otherwise a topic
	Name       string
	Prefixed   bool // Name is a prefix; otherwise it is the exact name
	Operations []kadm.ACLOperation
}

// AgentACLRules are what an agent may do (design.md §4). Everything else is denied, in particular
// reading other agents' DM topics and writing `agents.registry` and `agents.topics`. Group topics
// (`agents.topic.*`) are open chat channels: one prefix rule covers every present and future one.
func AgentACLRules(agentID string) []ACLRule {
	return []ACLRule{
		{Group: true, Name: agentID, Operations: []kadm.ACLOperation{kadm.OpRead, kadm.OpDescribe}},
		{Name: bus.DMTopic(agentID), Operations: []kadm.ACLOperation{kadm.OpRead, kadm.OpDescribe}},
		{Name: bus.DMTopicPrefix, Prefixed: true, Operations: []kadm.ACLOperation{kadm.OpWrite}},
		{Name: bus.GroupTopicPrefix, Prefixed: true, Operations: []kadm.ACLOperation{kadm.OpRead, kadm.OpWrite, kadm.OpDescribe}},
		{Name: bus.GeneralTopic, Operations: []kadm.ACLOperation{kadm.OpRead, kadm.OpWrite}},
		{Name: bus.RegistryTopic, Operations: []kadm.ACLOperation{kadm.OpRead}},
		{Name: bus.TopicsTopic, Operations: []kadm.ACLOperation{kadm.OpRead}},
		// Agents list open requests, so they read the topic as well as write it.
		{Name: bus.RequestsTopic, Operations: []kadm.ACLOperation{kadm.OpRead, kadm.OpWrite}},
	}
}

func (rule ACLRule) builder(principal string) *kadm.ACLBuilder {
	builder := kadm.NewACLs().Allow(principal).AllowHosts("*").Operations(rule.Operations...)
	if rule.Group {
		builder = builder.Groups(rule.Name)
	} else {
		builder = builder.Topics(rule.Name)
	}
	if rule.Prefixed {
		return builder.ResourcePatternType(kadm.ACLPatternPrefixed)
	}
	return builder.ResourcePatternType(kadm.ACLPatternLiteral)
}

// AllowRules creates the rules for the user. Creating an ACL that exists is not an error.
func (admin *Admin) AllowRules(ctx context.Context, user string, rules []ACLRule) error {
	for _, rule := range rules {
		results, err := admin.CreateACLs(ctx, rule.builder("User:"+user))
		if err != nil {
			return fmt.Errorf("creating ACL for %s: %w", rule.Name, err)
		}
		for _, result := range results {
			if result.Err != nil {
				return fmt.Errorf("creating ACL for %s: %w", rule.Name, result.Err)
			}
		}
	}
	return nil
}
