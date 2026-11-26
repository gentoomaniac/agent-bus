// Package busd is the minimal control service of decision 41 (docs/design/busd.md): it keeps the pending
// enrollment requests and, when the owner approves one, builds the agent's certificate from the
// approved identity with the CA key and sets the agent up on the broker. It runs in the broker's
// container and has no HTTP interface; cmd/busd wires it to the bus.
package busd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/trust"
)

// Limits on what the shared enrollment login can make busd hold or do.
const (
	requestLifetime    = 24 * time.Hour // of a pending request, and again of a decision, which the agent collects
	maxPending         = 100
	maxPendingPerAgent = 3
	maxEntries         = 500 // pending and decided; the oldest decided ones go first
	requestInterval    = 6 * time.Second
	requestBurst       = 10

	enrollDir    = "enroll"
	autosignFile = "autosign"
	stateFile    = "state.json"
)

// Registry is what busd needs of the broker: the registry, the read cursors and the accounts.
// *broker.Admin implements it.
type Registry interface {
	trust.CatalogueSource
	Agents(ctx context.Context) ([]bus.AgentRecord, error)
	PutAgentRecord(ctx context.Context, record bus.AgentRecord) error
	CreateReadCursor(ctx context.Context, agentID string, channel bus.Channel, afterSequence uint64) error
	LastSequence(ctx context.Context) (uint64, error)
	EnsureCertAccount(ctx context.Context, agentID, certID string) error
}

// Config is what a Service is made of.
type Config struct {
	Registry  Registry
	CA        *tlsinit.CA
	CACertPEM []byte // handed to approved agents, who compare it with the CA they trust
	ConfigDir string // holds enroll/ (state, autosign secret) and directory/
	// LockMutations takes the lock shared with agent-busctl --direct for as long as an approval
	// changes the registry and the accounts. Nil: no lock, for tests.
	LockMutations func() (unlock func(), err error)
	CertValidity  time.Duration
	Now           func() time.Time // default time.Now
	// DirectoryChanged is told when an approval has finished, so the published directory is
	// rebuilt at once rather than at its next round. Nil: nobody is told.
	DirectoryChanged func()
}

// Service is the enrollment and approval logic. One mutex serialises everything, a certificate
// approval included: an owner approves one request at a time.
type Service struct {
	config  Config
	limiter *rate.Limiter

	mu      sync.Mutex
	entries map[string]*entry // by request ID
}

// entry is one request and what became of it. Pending entries are the owner's queue; decided ones
// stay until the agent has had time to collect the answer.
type entry struct {
	Request     enroll.Request `json:"request"` // without the autosign secret
	SubmittedAt time.Time      `json:"submitted_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Status      enroll.Status  `json:"status"`
	Message     string         `json:"message,omitempty"`
	Certificate string         `json:"certificate,omitempty"`
	// CursorsAfter is the log position the agent's read cursors start after, taken by the first
	// attempt to approve, so that a repeated approval starts them at the same place.
	CursorsAfter *uint64 `json:"cursors_after,omitempty"`
	// Approving is set, on disk, before an approval changes anything, and cleared by the request's
	// decision: when the approval has finished, the account's reload included, or when the request
	// is rejected or expires. While the request is pending with it set, the agent is left out of
	// the published directory (unfinishedApprovals): its record may be ahead of its login.
	Approving bool `json:"approving,omitempty"`

	verified enroll.Verified
}

// New creates the service and reads the state a former run left.
func New(config Config) (*Service, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	service := &Service{
		config:  config,
		limiter: rate.NewLimiter(rate.Every(requestInterval), requestBurst),
		entries: map[string]*entry{},
	}
	return service, service.load()
}

func (service *Service) path(name string) string {
	return filepath.Join(service.config.ConfigDir, enrollDir, name)
}

// HandleRequest takes a request from the enrollment login. It proves nothing about the sender:
// the request is checked as a stranger's, and the answer holds public data only.
func (service *Service) HandleRequest(ctx context.Context, data []byte) enroll.Reply {
	if !service.limiter.Allow() {
		return enroll.Reply{Status: enroll.StatusBusy, Message: "too many enrollment requests, try again in a minute"}
	}
	verified, err := enroll.Verify(data)
	if err != nil {
		return enroll.Reply{Status: enroll.StatusRejected, Message: err.Error()}
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.config.Now()
	service.expire(now)

	id := verified.ID()
	if known, found := service.entries[id]; found {
		return service.reply(known) // the same request again: a client that lost the answer
	}
	identity, err := service.classify(ctx, verified)
	if err != nil {
		return refusal(err)
	}
	if reply, full := service.makeRoom(verified); full {
		return reply
	}

	stored := verified.Request
	stored.Secret = ""
	added := &entry{Request: stored, SubmittedAt: now, ExpiresAt: now.Add(requestLifetime), Status: enroll.StatusPending, verified: verified}
	service.entries[id] = added
	log.Info().Msgf("busd: enrollment request %s from %s for %s (%s), signing key %s", added.code(), verified.Hostname, verified.AgentID, identity, verified.SigningFingerprint)

	if identity != enroll.IdentityReplaces && service.autosignMatches(verified.Secret) {
		_, err := service.approve(ctx, added, false, "autosigned")
		// Saved first: the rebuild that the announcement starts reads whether it is still approving.
		service.saveOrLog()
		if err != nil {
			log.Error().Msgf("busd: autosign of %s failed, it stays pending: %v", added.code(), err)
		} else {
			service.directoryChanged()
		}
		return service.reply(added)
	}
	service.saveOrLog()
	return service.reply(added)
}

// HandleStatus answers an agent that asks how its request is doing.
func (service *Service) HandleStatus(ctx context.Context, data []byte) enroll.Reply {
	var query enroll.StatusQuery
	if len(data) > enroll.MaxRequestBytes || json.Unmarshal(data, &query) != nil {
		return enroll.Reply{Status: enroll.StatusRejected, Message: "not a status query"}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expire(service.config.Now())
	known, found := service.entries[query.RequestID]
	if !found {
		return enroll.Reply{Status: enroll.StatusUnknown, RequestID: query.RequestID}
	}
	return service.reply(known)
}

// PendingCount is the number of requests waiting for the owner.
func (service *Service) PendingCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expire(service.config.Now())
	return len(service.pending())
}

// List is the owner's queue, oldest first, with what approving each would do to its ID.
func (service *Service) List(ctx context.Context) enroll.ListReply {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expire(service.config.Now())
	var reply enroll.ListReply
	for _, request := range service.pending() {
		identity, err := service.classify(ctx, request.verified)
		if err != nil {
			identity = "unknown: " + err.Error()
		}
		reply.Pending = append(reply.Pending, enroll.Pending{
			RequestID: request.verified.ID(), Code: request.code(), AgentID: request.Request.AgentID, Hostname: request.Request.Hostname,
			SigningFingerprint: request.verified.SigningFingerprint, TLSFingerprint: request.verified.TLSFingerprint,
			SubmittedAt: request.SubmittedAt, ExpiresAt: request.ExpiresAt, Identity: identity,
		})
	}
	return reply
}

// Agents is the registry, for the monitor: its login cannot read CONTROL's JetStream itself.
func (service *Service) Agents(ctx context.Context) enroll.RegistryReply {
	agents, err := service.config.Registry.Agents(ctx)
	if err != nil {
		return enroll.RegistryReply{Error: err.Error()}
	}
	return enroll.RegistryReply{Agents: agents}
}

// Groups is the catalogue of group channels, for the monitor, without the channels busd is still
// creating.
func (service *Service) Groups(ctx context.Context) enroll.CatalogueReply {
	groups, err := service.config.Registry.GroupChannels(ctx)
	if err != nil {
		return enroll.CatalogueReply{Error: err.Error()}
	}
	return enroll.CatalogueReply{Groups: slices.DeleteFunc(groups, func(group bus.TopicRecord) bool { return group.Pending })}
}

// Sign approves one pending request, named by ID, code or agent ID. A request that would replace
// an identity with another key needs command.Replace.
func (service *Service) Sign(ctx context.Context, command enroll.SignCommand) enroll.CommandReply {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expire(service.config.Now())
	request, err := service.findPending(command.Ref)
	if err != nil {
		return enroll.CommandReply{Error: err.Error()}
	}
	message, err := service.approve(ctx, request, command.Replace, "approved by the owner")
	service.saveOrLog()
	if err != nil {
		return enroll.CommandReply{Error: err.Error()}
	}
	service.directoryChanged()
	return enroll.CommandReply{Message: message}
}

func (service *Service) directoryChanged() {
	if service.config.DirectoryChanged != nil {
		service.config.DirectoryChanged()
	}
}

// Reject refuses one pending request; the agent learns the reason.
func (service *Service) Reject(command enroll.RejectCommand) enroll.CommandReply {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expire(service.config.Now())
	request, err := service.findPending(command.Ref)
	if err != nil {
		return enroll.CommandReply{Error: err.Error()}
	}
	request.decide(enroll.StatusRejected, cleanReason(command.Reason), service.config.Now())
	service.saveOrLog()
	log.Info().Msgf("busd: rejected enrollment request %s for %s: %s", request.code(), request.Request.AgentID, request.Message)
	return enroll.CommandReply{Message: fmt.Sprintf("rejected %s (%s)", request.Request.AgentID, request.code())}
}

func (request *entry) code() string { return request.verified.Code() }

// decide ends the request, and any approval of it that was under way: an approval that failed
// and is then rejected leaves the agent as it was before, which the directory shows again.
func (request *entry) decide(status enroll.Status, message string, now time.Time) {
	request.Status, request.Message = status, message
	request.Approving = false
	request.ExpiresAt = now.Add(requestLifetime)
}

func cleanReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, reason)
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if reason == "" {
		return "rejected by the owner"
	}
	return reason
}

func refusal(err error) enroll.Reply {
	if errors.Is(err, errRegistry) {
		return enroll.Reply{Status: enroll.StatusBusy, Message: err.Error()}
	}
	return enroll.Reply{Status: enroll.StatusRejected, Message: err.Error()}
}

var errRegistry = errors.New("the registry cannot be read")

// classify says what approving the request would do to its agent ID, and refuses an ID that was
// removed: a removed ID is never reused, so certificates issued for it cannot speak for a new agent.
func (service *Service) classify(ctx context.Context, request enroll.Verified) (string, error) {
	records, err := service.config.Registry.Agents(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errRegistry, err)
	}
	index := slices.IndexFunc(records, func(record bus.AgentRecord) bool { return record.AgentID == request.AgentID })
	if index < 0 {
		return enroll.IdentityNew, nil
	}
	record := records[index]
	switch {
	case record.Removed():
		return "", fmt.Errorf("%s was removed and its ID is never used again", request.AgentID)
	case record.PublicKey == request.PublicKey && !currentKeyRevoked(record):
		return enroll.IdentitySameKey, nil
	}
	return enroll.IdentityReplaces, nil
}

func currentKeyRevoked(record bus.AgentRecord) bool {
	return len(record.Keys) > 0 && record.Keys[len(record.Keys)-1].Revoked
}

// makeRoom supersedes an earlier pending request of the same agent ID and key (the machine asked
// again) and refuses when the queue is full.
func (service *Service) makeRoom(request enroll.Verified) (enroll.Reply, bool) {
	forAgent := 0
	for _, queued := range service.pending() {
		if queued.Request.AgentID != request.AgentID {
			continue
		}
		if queued.Request.PublicKey == request.PublicKey {
			delete(service.entries, queued.verified.ID())
			log.Info().Msgf("busd: enrollment request %s superseded by a newer one of %s", queued.code(), request.AgentID)
			continue
		}
		forAgent++
	}
	switch {
	case len(service.pending()) >= maxPending:
		return enroll.Reply{Status: enroll.StatusBusy, Message: "too many requests are waiting for the owner"}, true
	case forAgent >= maxPendingPerAgent:
		return enroll.Reply{Status: enroll.StatusBusy, Message: "too many requests for this ID are waiting for the owner"}, true
	}
	return enroll.Reply{}, false
}

// pending is the undecided entries, oldest first.
func (service *Service) pending() []*entry {
	var pending []*entry
	for _, request := range service.entries {
		if request.Status == enroll.StatusPending {
			pending = append(pending, request)
		}
	}
	slices.SortFunc(pending, func(a, b *entry) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	return pending
}

// expire ends the requests whose time is up: a pending one is rejected as expired (the agent
// still hears why), a decided one is forgotten. Past maxEntries the oldest decided go too.
func (service *Service) expire(now time.Time) {
	for id, request := range service.entries {
		if now.Before(request.ExpiresAt) {
			continue
		}
		if request.Status == enroll.StatusPending {
			request.decide(enroll.StatusRejected, "the request expired without a decision", now)
			continue
		}
		delete(service.entries, id)
	}
	for len(service.entries) > maxEntries {
		var oldest *entry
		for _, request := range service.entries {
			if request.Status != enroll.StatusPending && (oldest == nil || request.SubmittedAt.Before(oldest.SubmittedAt)) {
				oldest = request
			}
		}
		if oldest == nil {
			return
		}
		delete(service.entries, oldest.verified.ID())
	}
}

// findPending resolves the owner's reference: the request ID, the approval code (with or without
// the dash) or the agent ID, which must then be unambiguous.
func (service *Service) findPending(ref string) (*entry, error) {
	normalised := strings.ToUpper(strings.ReplaceAll(ref, "-", ""))
	var matches []*entry
	for _, request := range service.pending() {
		if request.verified.ID() == ref || strings.ReplaceAll(request.code(), "-", "") == normalised || request.Request.AgentID == ref {
			matches = append(matches, request)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no pending request %q", ref)
	case 1:
		return matches[0], nil
	}
	codes := make([]string, len(matches))
	for i, match := range matches {
		codes[i] = match.code()
	}
	return nil, fmt.Errorf("%d pending requests match %q; name one by its code: %s", len(matches), ref, strings.Join(codes, ", "))
}

// approve turns a pending request into an agent that can log in with a certificate. The
// certificate is built from the approved agent ID and the CSR's public key alone. Every step can
// be repeated, so a failure leaves the request pending and the owner approves again.
func (service *Service) approve(ctx context.Context, request *entry, replace bool, how string) (string, error) {
	if service.config.LockMutations != nil {
		unlock, err := service.config.LockMutations()
		if err != nil {
			return "", err
		}
		defer unlock()
	}
	registry := service.config.Registry
	// Classified under the lock: an agent removed meanwhile has its tombstone by now, and is refused.
	identity, err := service.classify(ctx, request.verified)
	if err != nil {
		return "", err
	}
	if identity == enroll.IdentityReplaces && !replace {
		return "", fmt.Errorf("%s is registered with another key: approving replaces it and revokes the old key; confirm with --replace", request.Request.AgentID)
	}
	agentID := request.Request.AgentID
	// A new certID for each approval: it replaces the certificate of an earlier enrollment, which
	// stops working, and a repeat after a failed step signs again (the agent has not got this one).
	certID := broker.NewCertID()
	certificate, err := service.config.CA.SignCSR(broker.WithCertID(broker.AgentCertIdentity(agentID), certID), []byte(request.Request.CSR), service.config.CertValidity)
	if err != nil {
		return "", err
	}

	records, err := registry.Agents(ctx)
	if err != nil {
		return "", err
	}
	// A new agent starts at the end of the log, not in the history of `general`, at the end as the
	// first attempt found it. That position is on disk before anything changes: a crash between the
	// two cursors must not start the second one later and skip what was sent in between. An agent
	// that enrolls again keeps its cursors, and with them its position.
	if request.CursorsAfter == nil {
		last, err := registry.LastSequence(ctx)
		if err != nil {
			return "", err
		}
		request.CursorsAfter = &last
	}
	request.Approving = true
	if err := service.save(); err != nil {
		return "", fmt.Errorf("saving where the read cursors start: %w", err)
	}
	var record bus.AgentRecord
	if identity == enroll.IdentitySameKey {
		index := slices.IndexFunc(records, func(existing bus.AgentRecord) bool { return existing.AgentID == agentID })
		if index < 0 {
			return "", fmt.Errorf("%s is no longer in the registry", agentID)
		}
		record = records[index]
	} else {
		record = service.recordFor(records, request)
		if err := registry.PutAgentRecord(ctx, record); err != nil {
			return "", err
		}
	}
	for _, channel := range []bus.Channel{bus.DMChannel(agentID), bus.GeneralChannel()} {
		if err := registry.CreateReadCursor(ctx, agentID, channel, *request.CursorsAfter); err != nil {
			return "", fmt.Errorf("creating the read cursor of %s: %w", channel, err)
		}
	}
	if err := registry.EnsureCertAccount(ctx, agentID, certID); err != nil {
		return "", err
	}
	// Written whatever the identity is, from what the registry holds, and only now that the agent
	// can be reached: an approval repeated after a failure finds the new key in the registry (so it
	// counts as the same key) and would otherwise leave the old key in the directory.
	if err := trust.DirectoryPath(service.config.ConfigDir).Write(record); err != nil {
		return "", err
	}
	if catalogue, err := trust.BuildCatalogue(ctx, registry); err == nil {
		_ = trust.DirectoryPath(service.config.ConfigDir).WriteCatalogue(catalogue)
	}

	request.Certificate = string(certificate)
	request.decide(enroll.StatusApproved, "", service.config.Now())
	log.Info().Msgf("busd: %s enrollment request %s for %s (%s): certificate issued for %s, signing key %s",
		how, request.code(), agentID, identity, broker.WithCertID(broker.AgentCertIdentity(agentID), certID), request.verified.SigningFingerprint)
	return fmt.Sprintf("approved %s (%s): certificate issued, the agent now logs in with it", agentID, request.code()), nil
}

// recordFor is the registry record approval writes: a new agent's first key, or the existing
// record with its current key revoked and the requested one added (the owner confirmed that).
func (service *Service) recordFor(records []bus.AgentRecord, request *entry) bus.AgentRecord {
	now := service.config.Now().UTC()
	key := bus.KeyEntry{PublicKey: request.Request.PublicKey, Fingerprint: request.verified.SigningFingerprint, ValidFrom: now}
	record := bus.AgentRecord{AgentID: request.Request.AgentID, RegisteredAt: now}
	if index := slices.IndexFunc(records, func(existing bus.AgentRecord) bool { return existing.AgentID == record.AgentID }); index >= 0 {
		record = records[index]
		record.Keys = slices.Clone(record.Keys)
		if len(record.Keys) == 0 {
			record.Keys = []bus.KeyEntry{{PublicKey: record.PublicKey, Fingerprint: record.Fingerprint, ValidFrom: record.RegisteredAt}}
		}
		if current := &record.Keys[len(record.Keys)-1]; !current.Revoked {
			current.Revoked, current.RevokedAt = true, &now
		}
	}
	record.PublicKey, record.Fingerprint = key.PublicKey, key.Fingerprint
	record.Keys = append(record.Keys, key)
	return record
}

// reply is a request's state as the agent hears it. An approval carries the certificate and the
// CA certificate. It carries no directory: the reply is unsigned, and whoever has the shared login
// can overhear it and answer in its place; the agent syncs the signed directory from busd instead.
func (service *Service) reply(request *entry) enroll.Reply {
	reply := enroll.Reply{Status: request.Status, RequestID: request.verified.ID(), Code: request.code(), Message: request.Message, ExpiresAt: request.ExpiresAt}
	if request.Status != enroll.StatusApproved {
		return reply
	}
	reply.Certificate = request.Certificate
	reply.CA = string(service.config.CACertPEM)
	return reply
}

// autosignMatches says whether secret is the one in enroll/autosign. The file is read each time, so
// the owner turns autosigning on and off by creating and deleting it; an empty or missing file
// means off.
func (service *Service) autosignMatches(secret string) bool {
	expected, err := os.ReadFile(service.path(autosignFile))
	expected = bytes.TrimSpace(expected)
	if err != nil || len(expected) == 0 || secret == "" {
		return false
	}
	// Hashing first makes the comparison independent of the lengths.
	given, want := sha256.Sum256([]byte(secret)), sha256.Sum256(expected)
	return subtle.ConstantTimeCompare(given[:], want[:]) == 1
}

// load reads the state file; an entry that no longer verifies is dropped.
func (service *Service) load() error {
	data, err := os.ReadFile(service.path(stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var stored []*entry
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("reading %s: %w", service.path(stateFile), err)
	}
	for _, request := range stored {
		raw, err := json.Marshal(request.Request)
		if err == nil {
			request.verified, err = enroll.Verify(raw)
		}
		if err != nil {
			log.Warn().Msgf("busd: dropping a stored enrollment request that no longer verifies: %v", err)
			continue
		}
		service.entries[request.verified.ID()] = request
	}
	return nil
}

// save writes the state file: public data only (no secret, no private key), mode 0600, replaced
// atomically and synced to disk.
func (service *Service) save() error {
	stored := make([]*entry, 0, len(service.entries))
	for _, request := range service.entries {
		stored = append(stored, request)
	}
	slices.SortFunc(stored, func(a, b *entry) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return files.Write(service.path(stateFile), data, files.SecretMode, true)
}

// unfinishedApprovals are the agents whose approval began and has not finished, as the state file
// in configDir says. It reads the file, not a Service, so that busd's directory can ask under the
// mutation lock without the service's own lock, which an approval holds while it waits for that one.
func unfinishedApprovals(configDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, enrollDir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored []entry
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("reading the enrollment state: %w", err)
	}
	var agentIDs []string
	for _, request := range stored {
		if request.Approving && request.Status == enroll.StatusPending {
			agentIDs = append(agentIDs, request.Request.AgentID)
		}
	}
	return agentIDs, nil
}

// saveOrLog saves after an answer is decided. A failure is logged: the answer in hand is still
// right, only a restart would lose it.
func (service *Service) saveOrLog() {
	if err := service.save(); err != nil {
		log.Error().Msgf("busd: saving the enrollment state: %v", err)
	}
}
