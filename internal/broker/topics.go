package broker

import (
	"context"
	"errors"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

const (
	onePartition    = 1
	brokerDefaultRF = -1
)

// SystemTopics are the topics every bus has, with the retention of design.md §4.
var SystemTopics = []Topic{
	{Name: bus.RegistryTopic, Compacted: true},
	{Name: bus.GeneralTopic},
	{Name: bus.RequestsTopic, Compacted: true},
	{Name: bus.TopicsTopic, Compacted: true},
}

// Topic is a topic to create: compacted ones keep the latest record per key, all others keep everything.
type Topic struct {
	Name      string
	Compacted bool
}

func (topic Topic) configs() map[string]*string {
	if topic.Compacted {
		return map[string]*string{"cleanup.policy": ptr("compact")}
	}
	return map[string]*string{"retention.ms": ptr("-1")}
}

// CreateTopic creates a one-partition topic. It reports whether the topic is new: an existing topic is left as it is.
func CreateTopic(ctx context.Context, admin *kadm.Client, topic Topic) (created bool, err error) {
	_, err = admin.CreateTopic(ctx, onePartition, brokerDefaultRF, topic.configs(), topic.Name)
	if errors.Is(err, kerr.TopicAlreadyExists) {
		return false, nil
	}
	return err == nil, err
}

func ptr(value string) *string { return &value }
