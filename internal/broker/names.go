// Package broker is the NATS JetStream binding of the bus (docs/design/jetstream-binding.md). It implements the
// interfaces of package bus, and it is the only package that imports the NATS client library
// (the integration test aside).
package broker

import (
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
)

// The accounts and streams of docs/design/jetstream-binding.md.
const (
	busStream      = "BUS"
	presenceStream = "PRESENCE"
	ledgerStream   = "LEDGER"
	registryKV     = "registry"
	catalogueKV    = "catalogue"
)

// subject is the one place that maps a channel to the subject it is stored under in stream BUS.
func subject(channel bus.Channel) string {
	switch channel.Kind {
	case bus.General:
		return "general"
	case bus.Group:
		return "topic." + channel.Name
	default:
		return "dm." + bus.AgentToken(channel.Name)
	}
}

// presenceSubject is where an agent publishes its presence; the stream PRESENCE keeps only the last
// record of each subject.
func presenceSubject(agentToken string) string { return "presence." + agentToken }

// presenceReadSubject is the direct get that returns the last record published for the agent.
func presenceReadSubject(agentToken string) string {
	return "$JS.API.DIRECT.GET." + presenceStream + "." + presenceSubject(agentToken)
}

// consumerName is the durable consumer that holds an agent's read cursor on a channel (docs/design/jetstream-binding.md).
// The parts split at "__" because no token or channel name contains it. A DM channel is always
// the agent's own.
func consumerName(agentToken string, channel bus.Channel) string {
	switch channel.Kind {
	case bus.General:
		return agentToken + "__general"
	case bus.Group:
		return agentToken + "__g__" + channel.Name
	default:
		return agentToken + "__dm"
	}
}

// readCursorHolder is the agent token of a consumer that holds a read cursor on the channel, and
// false for any other consumer.
func readCursorHolder(name string, channel bus.Channel) (agentToken string, ok bool) {
	token, _, found := strings.Cut(name, "__")
	if !found || name != consumerName(token, channel) {
		return "", false
	}
	return token, true
}
