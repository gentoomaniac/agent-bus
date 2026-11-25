package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The fixed configuration of every agent consumer (docs/design/jetstream-binding.md). The agent cannot change it.
const (
	consumerAckWait       = time.Minute
	consumerMaxAckPending = 500 // the adapter's inbox buffer limit (docs/design/delivery.md)
	consumerMaxWaiting    = 8
	registryHistory       = 64 // revisions KV keeps per key; AgentRecord holds the key list itself
	duplicateWindow       = 24 * time.Hour
)

var busSubjects = []string{"dm.>", "general", "topic.>", "private.>"}

// Admin is the administrator's access to the broker as the two admin users, one connection per
// account, plus the server's authorization files (agent-busctl --direct). It implements bus.Admin.
type Admin struct {
	bus     *session // BUS, as bus-admin
	control *session // CONTROL, as control-admin
	server  Server
	release func() // see ReleaseOnClose
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
	if admin.release != nil {
		admin.release()
	}
}

// ReleaseOnClose makes Close call release, for a lock the caller holds as long as it administers.
func (admin *Admin) ReleaseOnClose(release func()) { admin.release = release }

// Setup applies the retention to BUS, the one stream of conversations. The membership ledger is
// not a log of messages and keeps everything.
func (admin *Admin) Setup(ctx context.Context, retention bus.Retention) error {
	// NATS refuses a max age below the dedupe window with an error that names neither; say which.
	if retention.MaxAge < 0 || retention.MaxAge > 0 && retention.MaxAge < duplicateWindow {
		return fmt.Errorf("max age %s: use 0 (keep forever) or at least the %s dedupe window", retention.MaxAge, duplicateWindow)
	}
	_, err := admin.bus.stream.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       busStream,
		Subjects:   busSubjects,
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     retention.MaxAge,
		MaxBytes:   retention.MaxBytes,
		MaxMsgs:    -1,
		Duplicates: duplicateWindow,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", busStream, admin.bus.explain(err))
	}

	// Only the last record of each agent's presence is kept; direct get lets agents read it
	// without a consumer.
	_, err = admin.bus.stream.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              presenceStream,
		Subjects:          []string{"presence.*"},
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
		MaxMsgsPerSubject: 1,
		AllowDirect:       true,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", presenceStream, admin.bus.explain(err))
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
	// Agents registered by an older release get the grants of this one (presence).
	return admin.updateAuth(ctx, func(*userTable) error { return nil })
}

func (admin *Admin) PutAgentRecord(ctx context.Context, record bus.AgentRecord) error {
	value, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := admin.putControlRecord(ctx, registryKV, bus.AgentToken(record.AgentID), value); err != nil {
		return fmt.Errorf("writing the registry (has `agent-busctl init` run?): %w", err)
	}
	return nil
}

func (admin *Admin) CreateAccount(ctx context.Context, agentID, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	token := bus.AgentToken(agentID)
	return admin.updateAuth(ctx, func(table *userTable) error {
		if _, exists := table.find(roleAgent, token); exists {
			return fmt.Errorf("%s already has an account", agentID)
		}
		table.set(authUser{role: roleAgent, name: token, mode: modePassword, passwordHash: hash})
		return nil
	})
}

// EnsureCertAccount gives the agent an account that logs in with the certificate that carries
// certID (WithCertID of AgentCertIdentity): it creates the account, switches an existing one from
// its password, or replaces the certificate of one that has a certificate, as one confirmed reload.
// Every other certificate of the agent stops working. The agent's grants are those of every agent.
func (admin *Admin) EnsureCertAccount(ctx context.Context, agentID, certID string) error {
	token := bus.AgentToken(agentID)
	return admin.updateAuth(ctx, func(table *userTable) error {
		table.set(authUser{role: roleAgent, name: token, mode: modeCert, certID: certID})
		return nil
	})
}

// AgentAccounts are the agents that have an account on the broker, from one read of auth.conf.
func (admin *Admin) AgentAccounts(context.Context) ([]string, error) {
	return admin.server.agentAccounts()
}

func (admin *Admin) AccountExists(_ context.Context, agentID string) (bool, error) {
	return admin.server.hasAgent(bus.AgentToken(agentID))
}

func (admin *Admin) DeleteAccount(ctx context.Context, agentID string) error {
	return admin.updateAuth(ctx, func(table *userTable) error {
		table.remove(roleAgent, bus.AgentToken(agentID))
		return nil
	})
}

func (admin *Admin) CreateGroupChannel(ctx context.Context, record bus.TopicRecord) (bool, error) {
	value, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	catalogue, err := admin.control.stream.KeyValue(ctx, catalogueKV)
	if err != nil {
		return false, fmt.Errorf("opening the catalogue (has `agent-busctl init` run?): %w", admin.control.explain(err))
	}
	_, err = catalogue.Create(ctx, record.Name, value)
	created := err == nil
	if err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
		return false, fmt.Errorf("writing the catalogue: %w", admin.control.explain(err))
	}
	// The grants are regenerated even for an existing channel, so a run that failed after the
	// catalogue write finishes the job when it is repeated.
	return created, admin.updateAuth(ctx, func(*userTable) error { return nil })
}

func (admin *Admin) DescribeGroupChannel(ctx context.Context, name, description string) error {
	catalogue, err := admin.control.stream.KeyValue(ctx, catalogueKV)
	if err != nil {
		return fmt.Errorf("opening the catalogue (has `agent-busctl init` run?): %w", admin.control.explain(err))
	}
	entry, err := catalogue.Get(ctx, name)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("there is no channel %s", name)
	}
	if err != nil {
		return admin.control.explain(err)
	}
	var record bus.TopicRecord
	if err := json.Unmarshal(entry.Value(), &record); err != nil {
		return fmt.Errorf("reading the catalogue entry of %s: %w", name, err)
	}
	record.Description, record.UpdatedAt = description, time.Now().UTC()
	value, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// Compare-and-set: a change made since the read fails rather than being overwritten.
	_, err = catalogue.Update(ctx, name, value, entry.Revision())
	return admin.control.explain(err)
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
func (admin *Admin) updateAuth(ctx context.Context, change func(table *userTable) error) error {
	return admin.server.UpdateAuth(ctx, func(body string) (string, error) {
		table, err := parseUserTable(body)
		if err != nil {
			return "", err
		}
		if err := change(&table); err != nil {
			return "", err
		}
		groups, err := admin.groupNames(ctx)
		if err != nil {
			return "", err
		}
		return table.render(groups), nil
	})
}

func (admin *Admin) groupNames(ctx context.Context) ([]string, error) {
	catalogue, err := admin.control.stream.KeyValue(ctx, catalogueKV)
	if err != nil {
		return nil, fmt.Errorf("opening the catalogue (has `agent-busctl init` run?): %w", admin.control.explain(err))
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
		return nil, fmt.Errorf("opening %s (has `agent-busctl init` run?): %w", bucket, admin.control.explain(err))
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

// AgentCertIdentity is the identity of the client certificates an agent logs in with. The URI SAN
// of a certificate is WithCertID of it, and is the NATS user name nats-server maps it to.
func AgentCertIdentity(agentID string) string {
	return certIdentity(roleAgent, bus.AgentToken(agentID))
}

// AdminCertIdentity is the same for an admin user (BusAdminUser or ControlAdminUser).
func AdminCertIdentity(adminUser string) string { return certIdentity(roleAdmin, adminUser) }

// SetAgentLogin changes how a registered agent logs in, as one confirmed reload that closes its
// connections. With a password hash it logs in with that password; with none, with the
// certificate that carries certID, and no other. Its ID, consumers and grants stay.
func (admin *Admin) SetAgentLogin(ctx context.Context, agentID, passwordHash, certID string) error {
	token := bus.AgentToken(agentID)
	return admin.updateAuth(ctx, func(table *userTable) error {
		if _, exists := table.find(roleAgent, token); !exists {
			return fmt.Errorf("%s has no account on the broker", agentID)
		}
		table.set(loginOf(roleAgent, token, passwordHash, certID))
		return nil
	})
}

// SetAdminLogin is SetAgentLogin for an admin user. The connections of that user close, so a
// caller that changes both admins changes BusAdminUser first: that change reads the catalogue
// through the CONTROL connection.
func (admin *Admin) SetAdminLogin(ctx context.Context, adminUser, passwordHash, certID string) error {
	change := func(table *userTable) error {
		if _, exists := table.find(roleAdmin, adminUser); !exists {
			return fmt.Errorf("%s is not an admin user of this broker", adminUser)
		}
		table.set(loginOf(roleAdmin, adminUser, passwordHash, certID))
		return nil
	}
	switch adminUser {
	case BusAdminUser:
		return admin.updateAuth(ctx, change)
	case ControlAdminUser:
		return admin.server.UpdateControlAuth(ctx, func(body string) (string, error) {
			table, err := parseUserTable(body)
			if err != nil {
				return "", err
			}
			if err := change(&table); err != nil {
				return "", err
			}
			return table.render(nil), nil
		})
	}
	return fmt.Errorf("%s is not an admin user of this broker", adminUser)
}

func loginOf(role authRole, name, passwordHash, certID string) authUser {
	if passwordHash == "" {
		return authUser{role: role, name: name, mode: modeCert, certID: certID}
	}
	return authUser{role: role, name: name, mode: modePassword, passwordHash: passwordHash}
}

func (admin *Admin) Presence(ctx context.Context, agentID string) ([]byte, error) {
	return admin.bus.readPresence(ctx, bus.AgentToken(agentID))
}

func (admin *Admin) WatchPresence(handler func(agentID string, record []byte)) (func(), error) {
	subscription, err := admin.bus.conn.Subscribe(presenceSubject("*"), func(message *nats.Msg) {
		token, _ := strings.CutPrefix(message.Subject, presenceSubject(""))
		handler(bus.AgentIDFromToken(token), message.Data)
	})
	if err == nil {
		err = admin.bus.conn.Flush() // so the server knows the subscription before this returns
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = subscription.Unsubscribe() }, nil
}

// Serve answers requests on a subject of CONTROL until stop is called: handle gets the request's
// body and returns the reply's. A message that carries no reply subject is not a request and is
// dropped. busd's own replies and publications have none, so one that is redirected onto a busd
// subject cannot pass for a request; the account's other users cannot publish there at all.
//
// The reply goes only to a literal subject below replyPrefix (the caller's inbox space). The
// subject is the sender's to name, so answering anywhere else would let it make busd publish
// wherever busd may, into a registry key or a JetStream API subject among them. Such a request is
// dropped; the first one is logged.
func (admin *Admin) Serve(subject, replyPrefix string, handle func(request []byte) []byte) (stop func(), err error) {
	return serve(admin.control, subject, replyPrefix+".*",
		func(_, reply string) bool { return isReplyBelow(reply, replyPrefix) },
		func(_ string, request []byte) []byte { return handle(request) })
}

// ServeAgents is Serve on the subjects prefix.<agent-token> of BUS (busd.directory.sync): each
// agent may publish on its own one only, so the subject says who asks, and handle is told the
// agent's ID. The reply goes only to a literal subject in that agent's own inbox space,
// _INBOX.<agent-token>.<token>...: busd's BUS login may publish anywhere in BUS, a DM subject among
// them, so no other reply is made.
func (admin *Admin) ServeAgents(prefix string, handle func(agentID string, request []byte) []byte) (stop func(), err error) {
	return serve(admin.bus, prefix+".*", "the caller's _INBOX.<agent-token>.*",
		func(subject, reply string) bool {
			_, allowed := callerOf(prefix, subject, reply)
			return allowed
		},
		func(subject string, request []byte) []byte {
			agentID, _ := callerOf(prefix, subject, "")
			return handle(agentID, request)
		})
}

// callerOf is the agent that asked on prefix.<agent-token>, and whether reply is a literal subject
// in its inbox space. An empty reply is not checked.
func callerOf(prefix, subject, reply string) (agentID string, allowed bool) {
	token, found := strings.CutPrefix(subject, prefix+".")
	agentID = bus.AgentIDFromToken(token)
	if !found || bus.ValidateAgentID(agentID) != nil || bus.AgentToken(agentID) != token {
		return "", false
	}
	return agentID, reply == "" || isReplyBelow(reply, "_INBOX."+token)
}

func serve(session *session, subject, allowed string, allows func(subject, reply string) bool, handle func(subject string, request []byte) []byte) (stop func(), err error) {
	var dropped atomic.Int64
	subscription, err := session.conn.Subscribe(subject, func(message *nats.Msg) {
		if message.Reply == "" {
			return
		}
		if !allows(message.Subject, message.Reply) {
			if dropped.Add(1) == 1 {
				log.Printf("busd: dropping a request on %s whose reply subject %q is outside %s (further ones are not logged)", message.Subject, message.Reply, allowed)
			}
			return
		}
		if err := message.Respond(handle(message.Subject, message.Data)); err != nil {
			log.Printf("busd: answering a request on %s: %v", message.Subject, err)
		}
	})
	if err == nil {
		err = session.conn.Flush() // so requests sent after this returns find the subscription
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = subscription.Unsubscribe() }, nil
}

// isReplyBelow says whether reply is a literal subject (no wildcard, no empty token) of the form
// prefix.<token>[.<token>...].
func isReplyBelow(reply, prefix string) bool {
	rest, found := strings.CutPrefix(reply, prefix+".")
	if !found {
		return false
	}
	for token := range strings.SplitSeq(rest, ".") {
		if token == "" || strings.ContainsAny(token, "*> \t\r\n") {
			return false
		}
	}
	return true
}

// PublishDirectoryHead announces busd's signed directory head to BUS (directory.SubjectHead).
func (admin *Admin) PublishDirectoryHead(head []byte) error {
	return admin.bus.conn.Publish(directory.SubjectHead, head)
}

// WatchDirectoryHeads hands every message on directory.SubjectHead in BUS to handler until stop
// is called: what the monitor refreshes its agents and channels on.
func (admin *Admin) WatchDirectoryHeads(handler func(head []byte)) (func(), error) {
	subscription, err := admin.bus.conn.Subscribe(directory.SubjectHead, func(message *nats.Msg) { handler(message.Data) })
	if err == nil {
		err = admin.bus.conn.Flush()
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = subscription.Unsubscribe() }, nil
}

// pendingEnrollments is the body of a message on enroll.SubjectPending.
type pendingEnrollments struct {
	Pending int `json:"pending"`
}

// PublishPendingEnrollments tells the monitor, which listens with WatchPendingEnrollments, how many
// enrollment requests wait for the owner. The count is all it learns.
func (admin *Admin) PublishPendingEnrollments(count int) error {
	body, err := json.Marshal(pendingEnrollments{Pending: count})
	if err != nil {
		return err
	}
	return admin.control.conn.Publish(enroll.SubjectPending, body)
}

func (admin *Admin) WatchPendingEnrollments(handler func(count int)) (func(), error) {
	subscription, err := admin.control.conn.Subscribe(enroll.SubjectPending, func(message *nats.Msg) {
		var body pendingEnrollments
		if json.Unmarshal(message.Data, &body) == nil {
			handler(body.Pending)
		}
	})
	if err == nil {
		err = admin.control.conn.Flush()
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = subscription.Unsubscribe() }, nil
}
