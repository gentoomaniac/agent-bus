package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// The fixed configuration of every agent consumer (design.md §4a). The agent cannot change it.
const (
	consumerAckWait       = time.Minute
	consumerMaxAckPending = 500 // the adapter's inbox buffer limit (§7)
	consumerMaxWaiting    = 8
	registryHistory       = 64 // revisions KV keeps per key; AgentRecord holds the key list itself
	duplicateWindow       = 24 * time.Hour
)

var busSubjects = []string{"dm.>", "general", "topic.>", "private.>"}

// Admin is the administrator's access to the broker as the two admin users, one connection per
// account, plus the server's authorization files (busctl --direct). It implements bus.Admin.
type Admin struct {
	bus     *session // BUS, as bus-admin
	control *session // CONTROL, as control-admin
	server  Server
}

var _ bus.Admin = (*Admin)(nil)

// NewAdmin connects to both accounts. busConn and controlConn name the two admin users.
func NewAdmin(busConn, controlConn Connection, server Server) (*Admin, error) {
	busSession, err := connect(busConn, "")
	if err != nil {
		return nil, err
	}
	controlSession, err := connect(controlConn, "")
	if err != nil {
		busSession.close()
		return nil, err
	}
	return &Admin{bus: busSession, control: controlSession, server: server}, nil
}

func (admin *Admin) Close() {
	admin.bus.close()
	admin.control.close()
}

func (admin *Admin) Setup(ctx context.Context) error {
	_, err := admin.bus.stream.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       busStream,
		Subjects:   busSubjects,
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     0,
		MaxBytes:   -1,
		MaxMsgs:    -1,
		Duplicates: duplicateWindow,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", busStream, admin.bus.explain(err))
	}

	// The ledger is a plain stream, not KV: KV keeps at most 64 revisions per key.
	_, err = admin.control.stream.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      ledgerStream,
		Subjects:  []string{"ledger.>"},
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    0,
		MaxBytes:  -1,
		MaxMsgs:   -1,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", ledgerStream, admin.control.explain(err))
	}
	for _, bucket := range []jetstream.KeyValueConfig{
		{Bucket: registryKV, History: registryHistory, Storage: jetstream.FileStorage},
		{Bucket: catalogueKV, History: 1, Storage: jetstream.FileStorage},
	} {
		if _, err := admin.control.stream.CreateOrUpdateKeyValue(ctx, bucket); err != nil {
			return fmt.Errorf("creating bucket %s: %w", bucket.Bucket, admin.control.explain(err))
		}
	}
	return nil
}

func (admin *Admin) PutAgentRecord(ctx context.Context, record bus.AgentRecord) error {
	value, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := admin.putControlRecord(ctx, registryKV, bus.AgentToken(record.AgentID), value); err != nil {
		return fmt.Errorf("writing the registry (has `busctl init` run?): %w", err)
	}
	return nil
}

func (admin *Admin) CreateAccount(ctx context.Context, agentID, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	token := bus.AgentToken(agentID)
	return admin.updateAuth(ctx, func(auth *busAuth) error {
		if _, exists := auth.agentHashes[token]; exists {
			return fmt.Errorf("%s already has an account", agentID)
		}
		auth.agentHashes[token] = hash
		return nil
	})
}

func (admin *Admin) AccountExists(_ context.Context, agentID string) (bool, error) {
	return admin.server.hasAgent(bus.AgentToken(agentID))
}

func (admin *Admin) DeleteAccount(ctx context.Context, agentID string) error {
	return admin.updateAuth(ctx, func(auth *busAuth) error {
		delete(auth.agentHashes, bus.AgentToken(agentID))
		return nil
	})
}

func (admin *Admin) CreateGroupChannel(ctx context.Context, name, description string) (bool, error) {
	value, err := json.Marshal(bus.TopicRecord{Name: name, Description: description, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return false, err
	}
	created, err := admin.putControlRecord(ctx, catalogueKV, name, value)
	if err != nil {
		return false, fmt.Errorf("writing the catalogue (has `busctl init` run?): %w", err)
	}
	// The grants are regenerated even for an existing channel, so a run that failed after the
	// catalogue write finishes the job when it is repeated.
	return created, admin.updateAuth(ctx, func(*busAuth) error { return nil })
}

func (admin *Admin) CreateReadCursor(ctx context.Context, agentID string, channel bus.Channel, afterSequence uint64) error {
	if channel.Kind == bus.DM && channel.Name != agentID {
		return fmt.Errorf("%s cannot read %s: an agent reads only its own DM channel", agentID, channel)
	}
	config := jetstream.ConsumerConfig{
		Durable:       consumerName(bus.AgentToken(agentID), channel),
		FilterSubject: subject(channel),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       consumerAckWait,
		MaxDeliver:    -1,
		MaxAckPending: consumerMaxAckPending,
		MaxWaiting:    consumerMaxWaiting,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	}
	if afterSequence > 0 {
		config.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		config.OptStartSeq = afterSequence + 1
	}
	_, err := admin.bus.stream.CreateConsumer(ctx, busStream, config)
	if errors.Is(err, jetstream.ErrConsumerExists) {
		return nil
	}
	return admin.bus.explain(err)
}

func (admin *Admin) DeleteReadCursor(ctx context.Context, agentID string, channel bus.Channel) error {
	err := admin.bus.stream.DeleteConsumer(ctx, busStream, consumerName(bus.AgentToken(agentID), channel))
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil
	}
	return admin.bus.explain(err)
}

func (admin *Admin) Agents(ctx context.Context) ([]bus.AgentRecord, error) {
	return listControlRecords[bus.AgentRecord](ctx, admin, registryKV)
}

func (admin *Admin) GroupChannels(ctx context.Context) ([]bus.TopicRecord, error) {
	return listControlRecords[bus.TopicRecord](ctx, admin, catalogueKV)
}

func (admin *Admin) ReadCursorHolders(ctx context.Context, channel bus.Channel) ([]string, error) {
	stream, err := admin.bus.stream.Stream(ctx, busStream)
	if err != nil {
		return nil, admin.bus.explain(err)
	}
	var holders []string
	names := stream.ConsumerNames(ctx)
	for name := range names.Name() {
		if token, ok := readCursorHolder(name, channel); ok {
			holders = append(holders, bus.AgentIDFromToken(token))
		}
	}
	return holders, admin.bus.explain(names.Err())
}

func (admin *Admin) LastSequence(ctx context.Context) (uint64, error) {
	stream, err := admin.bus.stream.Stream(ctx, busStream)
	if err != nil {
		return 0, admin.bus.explain(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, admin.bus.explain(err)
	}
	return info.State.LastSeq, nil
}

// updateAuth changes the users of BUS and regenerates every agent's grants from the catalogue, as
// one confirmed reload. The catalogue is read under the server's lock, so a group channel created
// meanwhile is never left out.
func (admin *Admin) updateAuth(ctx context.Context, change func(auth *busAuth) error) error {
	return admin.server.UpdateAuth(ctx, func(body string) (string, error) {
		auth := parseBusAuth(body)
		if err := change(&auth); err != nil {
			return "", err
		}
		groups, err := admin.groupNames(ctx)
		if err != nil {
			return "", err
		}
		return auth.render(groups), nil
	})
}

func (admin *Admin) groupNames(ctx context.Context) ([]string, error) {
	catalogue, err := admin.control.stream.KeyValue(ctx, catalogueKV)
	if err != nil {
		return nil, fmt.Errorf("opening the catalogue (has `busctl init` run?): %w", admin.control.explain(err))
	}
	names, err := catalogue.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	return names, admin.control.explain(err)
}

// putControlRecord writes a record of a KV bucket in CONTROL with compare-and-set: it creates the
// key, or replaces the revision it has just read.
func (admin *Admin) putControlRecord(ctx context.Context, bucket, key string, value []byte) (created bool, err error) {
	kv, err := admin.control.stream.KeyValue(ctx, bucket)
	if err != nil {
		return false, admin.control.explain(err)
	}
	current, err := kv.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		_, err = kv.Create(ctx, key, value)
		return err == nil, admin.control.explain(err)
	}
	if err != nil {
		return false, admin.control.explain(err)
	}
	_, err = kv.Update(ctx, key, value, current.Revision())
	return false, admin.control.explain(err)
}

// listControlRecords reads every value of a KV bucket in CONTROL as a T.
func listControlRecords[T any](ctx context.Context, admin *Admin, bucket string) ([]T, error) {
	kv, err := admin.control.stream.KeyValue(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("opening %s (has `busctl init` run?): %w", bucket, admin.control.explain(err))
	}
	keys, err := kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	if err != nil {
		return nil, admin.control.explain(err)
	}
	var records []T
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, admin.control.explain(err)
		}
		var record T
		if err := json.Unmarshal(entry.Value(), &record); err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", bucket, key, err)
		}
		records = append(records, record)
	}
	return records, nil
}
