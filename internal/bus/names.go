// Package bus holds the broker-neutral model of the agent bus (design.md §3, §4): agent IDs,
// channels, the records the bus stores, and the two interfaces to the broker (§2). The binding
// that implements them is internal/broker; nothing else knows how channels map to broker names.
package bus

import (
	"fmt"
	"regexp"
	"strings"
)

// maxNameLength keeps every name derived from an agent ID or channel name (subjects, consumer
// names) far below the broker's limits.
const maxNameLength = 128

var (
	agentIDPattern   = regexp.MustCompile(`^[a-z0-9-]+@[a-z0-9-]+$`)
	groupNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// ValidateAgentID checks the `<name>@<owner>` format of §3.
func ValidateAgentID(agentID string) error {
	if len(agentID) > maxNameLength || !agentIDPattern.MatchString(agentID) {
		return fmt.Errorf("invalid agent ID %q: want <name>@<owner>, lowercase [a-z0-9-], at most %d characters", agentID, maxNameLength)
	}
	return nil
}

// ValidateGroupName checks the name of a group channel.
func ValidateGroupName(name string) error {
	if len(name) > maxNameLength || !groupNamePattern.MatchString(name) {
		return fmt.Errorf("invalid channel name %q: lowercase [a-z0-9-], at most %d characters", name, maxNameLength)
	}
	return nil
}

// AgentToken is the one place that maps an agent ID to the form the broker's names can carry:
// `lib-x@bob` becomes `lib-x_bob`. A token has exactly one '_' because agent IDs contain none,
// so names built from tokens and a "__" separator split unambiguously (§4a).
func AgentToken(agentID string) string {
	return strings.Replace(agentID, "@", "_", 1)
}

// ChannelKind says which of the bus's channels a Channel names (§4).
type ChannelKind int

const (
	// General is the channel every agent reads and writes.
	General ChannelKind = iota
	// Group is an open channel any agent may read and write; Name is its name.
	Group
	// DM is one agent's inbox; Name is its agent ID.
	DM
)

// Channel names a channel in bus terms: never a broker subject or topic.
type Channel struct {
	Kind ChannelKind
	Name string
}

// GeneralChannel is the channel `general`.
func GeneralChannel() Channel { return Channel{Kind: General} }

// GroupChannel is the open channel with the given name.
func GroupChannel(name string) Channel { return Channel{Kind: Group, Name: name} }

// DMChannel is the inbox of the given agent.
func DMChannel(agentID string) Channel { return Channel{Kind: DM, Name: agentID} }

func (channel Channel) String() string {
	switch channel.Kind {
	case General:
		return "general"
	case Group:
		return "channel " + channel.Name
	default:
		return "DM " + channel.Name
	}
}
