// Package bus holds the naming rules of the agent bus: agent IDs, topic names
// and the fixed system topics (design.md §3, §4).
package bus

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	RegistryTopic = "agents.registry"
	GeneralTopic  = "agents.general"
	RequestsTopic = "agents.requests"
	TopicsTopic   = "agents.topics"

	// The prefixes are also the prefixes of the ACLs that open these topics to every agent.
	DMTopicPrefix    = "agents.dm."
	GroupTopicPrefix = "agents.topic."

	// Kafka allows 249 characters per topic name; the prefixes use up to 13.
	maxNameLength = 128
)

var (
	agentIDPattern   = regexp.MustCompile(`^[a-z0-9-]+@[a-z0-9-]+$`)
	topicNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// ValidateAgentID checks the `<name>@<owner>` format of §3.
func ValidateAgentID(agentID string) error {
	if len(agentID) > maxNameLength || !agentIDPattern.MatchString(agentID) {
		return fmt.Errorf("invalid agent ID %q: want <name>@<owner>, lowercase [a-z0-9-], at most %d characters", agentID, maxNameLength)
	}
	return nil
}

// ValidateTopicName checks the name of a group topic (without the `agents.topic.` prefix).
func ValidateTopicName(name string) error {
	if len(name) > maxNameLength || !topicNamePattern.MatchString(name) {
		return fmt.Errorf("invalid topic name %q: lowercase [a-z0-9-], at most %d characters", name, maxNameLength)
	}
	return nil
}

// DMTopic is the one place that maps an agent ID to its inbox topic.
// Kafka topic names cannot contain '@', so `lib-x@bob` becomes `agents.dm.lib-x.bob`.
// The mapping is unambiguous because agent IDs contain no '.'.
func DMTopic(agentID string) string {
	return DMTopicPrefix + strings.Replace(agentID, "@", ".", 1)
}

// GroupTopic is the Kafka topic of a named group topic.
func GroupTopic(name string) string {
	return GroupTopicPrefix + name
}
