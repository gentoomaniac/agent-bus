package broker

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"golang.org/x/crypto/bcrypt"
)

// The two admin users. NATS user names are server-wide, so the admin of each account has its own.
const (
	BusAdminUser     = "bus-admin"
	ControlAdminUser = "control-admin"
)

// agentBusdMethods are the busd calls an agent may make (docs/design/jetstream-binding.md). Nothing answers them before
// milestone 3; the grants are in place so the per-role matrix is complete.
var agentBusdMethods = []string{
	"nonce", "channel.create", "channel.list", "channel.members", "channel.membership",
	"channel.invite", "channel.remove", "channel.leave", "channel.join", "history",
	"registry.get", "registry.changes", "catalogue.list", "agent.rotate",
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
	// roleMonitor and roleEnrollment are reserved for phase B of the mTLS login (docs/design/access-control.md).
	// A file that has them is read and written back unchanged; they have no access until their
	// grants are defined.
	roleMonitor    authRole = "monitor"
	roleEnrollment authRole = "enrollment"
)

// authMode is how an entry logs in: with a password, or with a client certificate that
// nats-server maps to the entry (verify_and_map).
type authMode string

const (
	modePassword authMode = "password"
	modeCert     authMode = "cert"
)

// certIdentityScheme starts the one URI SAN of every client certificate, which is also the NATS
// user name of its entry: agentbus:<role>/<name>, with the agent token or the admin user's name
// as name. nats-server maps the certificate to the user of that name (verify_and_map).
const certIdentityScheme = "agentbus:"

// certIdentity is the URI SAN, and NATS user name, of a certificate login.
func certIdentity(role authRole, name string) string {
	return certIdentityScheme + string(role) + "/" + name
}

// authUser is one entry of an authorization file. name is the agent token or the admin user's
// name; the NATS user name follows from it and the mode.
type authUser struct {
	role         authRole
	name         string
	mode         authMode
	passwordHash string // modePassword only
}

func (user authUser) natsName() string {
	if user.mode == modeCert {
		return certIdentity(user.role, user.name)
	}
	return user.name
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
	if !slices.Contains([]authRole{roleAgent, roleAdmin, roleMonitor, roleEnrollment}, role) {
		return authUser{}, fmt.Errorf("unknown role %q", role)
	}
	if passwordHash != "" {
		return authUser{role: role, name: natsName, mode: modePassword, passwordHash: passwordHash}, nil
	}
	name, found := strings.CutPrefix(natsName, certIdentityScheme+string(role)+"/")
	if !found {
		return authUser{}, fmt.Errorf("user %q has no password and is not a certificate identity of role %s", natsName, role)
	}
	return authUser{role: role, name: name, mode: modeCert}, nil
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
	return publish, []string{"_INBOX." + token + ".>"}
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
	default:
		return `{publish: {deny: [">"]}, subscribe: {deny: [">"]}}`
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
