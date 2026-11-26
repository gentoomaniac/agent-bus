package broker

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/channelapi"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/directory"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/enroll"
	"golang.org/x/crypto/bcrypt"
)

// The password logins besides the agents'. NATS user names are server-wide, so each account has
// its own admin, and the monitor has one login per account. busd's is the one it serves CONTROL
// with; it still uses bus-admin for BUS.
const (
	BusAdminUser       = "bus-admin"
	ControlAdminUser   = "control-admin"
	BusdUser           = "busd"
	MonitorBusUser     = "monitor-bus"
	MonitorControlUser = "monitor-control"
)

// principals says where each of those logins lives and what it is for.
var principals = map[string]struct {
	role authRole
	file string // the authorization file of its account
}{
	BusAdminUser:       {roleAdmin, authFile},
	ControlAdminUser:   {roleAdmin, controlAuthFile},
	BusdUser:           {roleBusd, controlAuthFile},
	MonitorBusUser:     {roleMonitor, authFile},
	MonitorControlUser: {roleMonitor, controlAuthFile},
}

// agentBusdMethods are the busd calls of docs/design/jetstream-binding.md an agent may make. Nothing
// answers them yet; the grants are in place so the per-role matrix is complete. The calls busd
// answers have a subject per agent, below.
var agentBusdMethods = []string{
	"nonce", "history", "registry.get", "registry.changes", "catalogue.list", "agent.rotate",
}

// HashPassword is how a password is stored in the generated authorization files.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// authRole says what an entry of an authorization file is for, and so which grants it gets.
type authRole string

const (
	roleAgent authRole = "agent"
	roleAdmin authRole = "admin"
	// roleEnrollment is the shared login of new machines, roleBusd the login busd serves CONTROL
	// with, roleMonitor the monitor's read-only logins (docs/design/access-control.md). A
	// monitor entry that is none of the monitor's two logins has no access.
	roleMonitor    authRole = "monitor"
	roleEnrollment authRole = "enrollment"
	roleBusd       authRole = "busd"
)

// authMode is how an entry logs in: with a password, or with a client certificate that
// nats-server maps to the entry (verify_and_map).
type authMode string

const (
	modePassword authMode = "password"
	modeCert     authMode = "cert"
)

// certIdentityScheme starts the one URI SAN of every client certificate, which is also the NATS
// user name of its entry: agentbus:<role>/<name>/<certID>, with the agent token or the admin
// user's name as name. nats-server maps the certificate to the user of that name
// (verify_and_map).
//
// The certID makes a certificate revocable. nats-server's pinned_certs is not applied to a
// WebSocket login in v2.15.3, and it has no revocation list, so the only way to take a
// certificate back is for its identity to name no user. A principal has one entry, which names
// the certID of its current certificate: issuing another one, replacing the identity, removing it
// or switching it back to the password rewrites the entry, and every earlier certificate maps to
// nothing. An entry written before certIDs has none (agentbus:<role>/<name>) and keeps working
// until its certificate is issued again.
const certIdentityScheme = "agentbus:"

// certIdentity is the identity a certificate login of the role and name carries, without the
// certID: what a client checks its certificate against (creds.MatchesIdentity).
func certIdentity(role authRole, name string) string {
	return certIdentityScheme + string(role) + "/" + name
}

// NewCertID makes the ID of a certificate: the one thing that tells it from an earlier one of
// the same principal.
func NewCertID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // the system's random source is broken
	}
	return hex.EncodeToString(raw)
}

// WithCertID is the URI SAN of the certificate with the given ID, for the principal whose
// identity is AgentCertIdentity or AdminCertIdentity.
func WithCertID(identity, certID string) string { return identity + "/" + certID }

// authUser is one entry of an authorization file. name is the agent token or the admin user's
// name; the NATS user name follows from it and the mode.
type authUser struct {
	role         authRole
	name         string
	mode         authMode
	passwordHash string // modePassword only
	certID       string // modeCert only: the current certificate; empty for one made before certIDs
}

func (user authUser) natsName() string {
	if user.mode != modeCert {
		return user.name
	}
	if user.certID == "" {
		return certIdentity(user.role, user.name)
	}
	return WithCertID(certIdentity(user.role, user.name), user.certID)
}

// userTable is the users of one authorization file. The file is the only place the password
// hashes and certificate mappings live, so reading it and writing it back must lose nothing.
type userTable []authUser

func (table userTable) find(role authRole, name string) (index int, found bool) {
	index = slices.IndexFunc(table, func(user authUser) bool { return user.role == role && user.name == name })
	return index, index >= 0
}

// set adds the user or replaces the one with the same role and name.
func (table *userTable) set(user authUser) {
	if index, found := table.find(user.role, user.name); found {
		(*table)[index] = user
		return
	}
	*table = append(*table, user)
}

func (table *userTable) remove(role authRole, name string) {
	if index, found := table.find(role, name); found {
		*table = slices.Delete(*table, index, index+1)
	}
}

// userLine matches an entry written by render. The trailing comment names the role; a file written
// before roles existed has none, and its entries are the two admins and password agents.
var userLine = regexp.MustCompile(`^\s*\{user: "([^"]+)"(?:, password: "([^"]+)")?.*?(?:# (\w+))?\s*$`)

func parseUserTable(body string) (userTable, error) {
	var table userTable
	for number, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{user:") {
			continue
		}
		match := userLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("line %d of the authorization file is not a user entry this tool wrote: %s", number+1, strings.TrimSpace(line))
		}
		user, err := parseUser(match[1], match[2], authRole(match[3]))
		if err != nil {
			return nil, fmt.Errorf("line %d of the authorization file: %w", number+1, err)
		}
		table = append(table, user)
	}
	return table, nil
}

func parseUser(natsName, passwordHash string, role authRole) (authUser, error) {
	if role == "" {
		role = roleAgent
		if natsName == BusAdminUser || natsName == ControlAdminUser {
			role = roleAdmin
		}
	}
	if !slices.Contains([]authRole{roleAgent, roleAdmin, roleMonitor, roleEnrollment, roleBusd}, role) {
		return authUser{}, fmt.Errorf("unknown role %q", role)
	}
	if passwordHash != "" {
		return authUser{role: role, name: natsName, mode: modePassword, passwordHash: passwordHash}, nil
	}
	rest, found := strings.CutPrefix(natsName, certIdentityScheme+string(role)+"/")
	if !found {
		return authUser{}, fmt.Errorf("user %q has no password and is not a certificate identity of role %s", natsName, role)
	}
	name, certID, _ := strings.Cut(rest, "/")
	return authUser{role: role, name: name, mode: modeCert, certID: certID}, nil
}

// render writes the users of an authorization file, by role and name (admins first). An agent
// gets the grants of docs/design/jetstream-binding.md for the given catalogued group channels.
func (table userTable) render(groups []string) string {
	sorted := slices.Clone(table)
	slices.SortFunc(sorted, func(a, b authUser) int {
		return cmp.Or(strings.Compare(string(a.role), string(b.role)), strings.Compare(a.name, b.name))
	})
	var out strings.Builder
	out.WriteString("users = [\n")
	for _, user := range sorted {
		fmt.Fprintf(&out, "  %s, # %s\n", userEntry(user, groups), user.role)
	}
	out.WriteString("]\n")
	return out.String()
}

// replaceUserEntry swaps the entry of the same role and name in a rendered body for the given
// user's (or adds it) and leaves every other line as it is: the grants of the others depend on
// the catalogue, which an offline caller cannot read.
func replaceUserEntry(body string, user authUser) (string, error) {
	if _, err := parseUserTable(body); err != nil {
		return "", err
	}
	entry := fmt.Sprintf("  %s, # %s", userEntry(user, nil), user.role)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "{user:") {
			continue
		}
		listed, err := parseUserTable(line)
		if err != nil {
			return "", err
		}
		if _, same := listed.find(user.role, user.name); same {
			lines[i] = entry
			return strings.Join(lines, "\n"), nil
		}
	}
	return strings.Replace(body, "users = [\n", "users = [\n"+entry+"\n", 1), nil
}

// agentPermissions are the subjects an agent may publish and subscribe to (docs/design/jetstream-binding.md). It receives only
// through its own consumers and replies only on its own inbox prefix, and it creates, updates and
// deletes no consumer: nothing below grants a $JS.API.CONSUMER.CREATE, DELETE or DURABLE subject.
func agentPermissions(token string, groups []string) (publish, subscribe []string) {
	publish = []string{"dm.*", "general", "topic.*"}
	consumers := []string{consumerName(token, bus.GeneralChannel()), consumerName(token, bus.DMChannel(""))}
	for _, group := range groups {
		consumers = append(consumers, consumerName(token, bus.GroupChannel(group)))
	}
	for _, consumer := range consumers {
		publish = append(publish,
			"$JS.API.CONSUMER.MSG.NEXT."+busStream+"."+consumer,
			"$JS.API.CONSUMER.INFO."+busStream+"."+consumer,
			"$JS.ACK."+busStream+"."+consumer+".>")
	}
	// Presence: write its own record, read anyone's (docs/design/presence.md). A core publish needs no
	// JetStream API grant; the stream stores whatever arrives on its subjects.
	publish = append(publish, presenceSubject(token), presenceReadSubject("*"))
	for _, method := range agentBusdMethods {
		publish = append(publish, "busd."+method)
	}
	// The directory sync (docs/design/directory-v2.md) and the channel requests
	// (docs/design/agent-channels.md) are the busd calls agents make, answered in their inbox. Each
	// agent has subjects of its own, so busd knows who asks, to answer only there and to limit each
	// agent on its own.
	publish = append(publish, directory.SyncSubject(token), channelapi.Subject(token))
	return publish, []string{"_INBOX." + token + ".>", directory.SubjectHead}
}

// userEntry is one line of a users list. The line format is what userLine reads back.
func userEntry(user authUser, groups []string) string {
	login := fmt.Sprintf("password: %q, ", user.passwordHash)
	connectionTypes := ""
	if user.mode == modeCert {
		login = ""
		// Only the mapped listener may log this user in, see deploy/nats-server.conf.
		connectionTypes = `, allowed_connection_types: ["WEBSOCKET"]`
	}
	return fmt.Sprintf("{user: %q, %spermissions: %s%s}", user.natsName(), login, permissions(user, groups), connectionTypes)
}

func permissions(user authUser, groups []string) string {
	switch user.role {
	case roleAdmin:
		return allowOnly([]string{">"}, []string{">"})
	case roleAgent:
		publish, subscribe := agentPermissions(user.name, groups)
		return allowOnly(publish, subscribe)
	case roleEnrollment:
		// It may ask busd for approval and hear the answers on its own inbox space, nothing else: no
		// stream, no JetStream API. The login is shared, so its replies hold public data only.
		return allowOnly([]string{enroll.SubjectRequest, enroll.SubjectStatus}, []string{enroll.InboxPrefix + ".>"})
	case roleBusd:
		return busdPermissions()
	case roleMonitor:
		switch user.name {
		case MonitorBusUser:
			return monitorBusPermissions()
		case MonitorControlUser:
			return monitorControlPermissions()
		}
	}
	return `{publish: {deny: [">"]}, subscribe: {deny: [">"]}}`
}

// busdPermissions are what busd needs in CONTROL and no more: it reads and writes the registry and
// the catalogue (channels agents create), answers requests into inbox space and announces the
// pending count. It cannot delete a stream, purge one or write the ledger, whatever subject a
// request tells it to reply on.
func busdPermissions() string {
	publish := slices.Concat(
		kvReadSubjects(registryKV), kvReadSubjects(catalogueKV),
		[]string{"$KV." + registryKV + ".>", "$KV." + catalogueKV + ".>", "_INBOX.>", enroll.SubjectPending})
	subscribe := []string{"_INBOX.>", enroll.SubjectRequest, enroll.SubjectStatus, enroll.SubjectList, enroll.SubjectSign, enroll.SubjectReject,
		enroll.SubjectMonitorRegistry, enroll.SubjectMonitorCatalogue}
	return allowOnly(publish, subscribe)
}

// monitorControlPermissions let the monitor ask busd for the registry and the catalogue and hear
// the pending count. It gets no JetStream API subject at all: a read grant such as a direct get
// lets its holder name a reply subject that writes, and NATS only rejects $JS.* ones.
func monitorControlPermissions() string {
	publish := []string{enroll.SubjectMonitorRegistry, enroll.SubjectMonitorCatalogue}
	// Only its own reply inbox: other replies in CONTROL (busd.ca.list) carry approval codes.
	return allowOnly(publish, []string{enroll.MonitorInboxPrefix + ".>", enroll.SubjectPending})
}

// monitorBusPermissions let the monitor read every channel through consumers of its own, read a
// stored record by its sequence (STREAM.MSG.GET; BUS has no AllowDirect, so no direct get), list who
// holds a read cursor, read presence and hear busd's directory heads. It publishes no message. JetStream subjects cannot name
// a consumer prefix, so these grants also reach the consumers of agents; the monitor sees all
// traffic anyway (docs/design/access-control.md).
func monitorBusPermissions() string {
	publish := []string{
		"$JS.API.STREAM.INFO." + busStream,
		"$JS.API.STREAM.MSG.GET." + busStream,
		"$JS.API.CONSUMER.CREATE." + busStream + ".>",
		"$JS.API.CONSUMER.INFO." + busStream + ".*",
		"$JS.API.CONSUMER.DELETE." + busStream + ".*",
		"$JS.API.CONSUMER.MSG.NEXT." + busStream + ".*",
		"$JS.API.CONSUMER.NAMES." + busStream,
		presenceReadSubject("*"),
	}
	return allowOnly(publish, []string{"_INBOX.>", presenceSubject("*"), directory.SubjectHead})
}

// kvReadSubjects are the JetStream API subjects that read a KV bucket: its stream's info, direct
// gets and ordered consumers for the key listing.
func kvReadSubjects(bucket string) []string {
	stream := "KV_" + bucket
	return []string{
		"$JS.API.STREAM.INFO." + stream,
		"$JS.API.DIRECT.GET." + stream, "$JS.API.DIRECT.GET." + stream + ".>",
		"$JS.API.CONSUMER.CREATE." + stream, "$JS.API.CONSUMER.CREATE." + stream + ".>",
		"$JS.API.CONSUMER.INFO." + stream + ".*",
		"$JS.API.CONSUMER.DELETE." + stream + ".*",
		"$JS.API.CONSUMER.MSG.NEXT." + stream + ".*",
	}
}

func allowOnly(publish, subscribe []string) string {
	return fmt.Sprintf("{publish: {allow: %s}, subscribe: {allow: %s}}", quoteList(publish), quoteList(subscribe))
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
