package broker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"golang.org/x/crypto/bcrypt"
)

// The two admin users. NATS user names are server-wide, so the admin of each account has its own.
const (
	BusAdminUser     = "bus-admin"
	ControlAdminUser = "control-admin"
)

// agentBusdMethods are the busd calls an agent may make (§4a). Nothing answers them before
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

// busAuth is what the BUS account's authorization file says: the admin's password hash and the
// hash of every registered agent, by agent token.
type busAuth struct {
	adminHash   string
	agentHashes map[string]string
}

// userLine matches a user in a file written by renderBusAuth: the file is the only place the
// password hashes live, so it has to be read back.
var userLine = regexp.MustCompile(`^\s*\{user: "([^"]+)", password: "([^"]+)"`)

// replaceAdminHash sets the admin's password hash and leaves every other user as it is.
func replaceAdminHash(body, hash string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if match := userLine.FindStringSubmatch(line); match != nil && match[1] == BusAdminUser {
			lines[i] = strings.Replace(line, match[2], hash, 1)
		}
	}
	return strings.Join(lines, "\n")
}

func parseBusAuth(body string) busAuth {
	auth := busAuth{agentHashes: map[string]string{}}
	for _, line := range strings.Split(body, "\n") {
		match := userLine.FindStringSubmatch(line)
		switch {
		case match == nil:
		case match[1] == BusAdminUser:
			auth.adminHash = match[2]
		default:
			auth.agentHashes[match[1]] = match[2]
		}
	}
	return auth
}

// render writes the users of BUS: the admin, and every agent with the grants of §4a for the
// given catalogued group channels.
func (auth busAuth) render(groups []string) string {
	var out strings.Builder
	out.WriteString("users = [\n")
	fmt.Fprintf(&out, "  %s,\n", userEntry(BusAdminUser, auth.adminHash, []string{">"}, []string{">"}))

	tokens := make([]string, 0, len(auth.agentHashes))
	for token := range auth.agentHashes {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		publish, subscribe := agentPermissions(token, groups)
		fmt.Fprintf(&out, "  %s,\n", userEntry(token, auth.agentHashes[token], publish, subscribe))
	}
	out.WriteString("]\n")
	return out.String()
}

// renderControlAuth writes the users of CONTROL: only its admin. busd's own connection joins in
// milestone 3.
func renderControlAuth(adminHash string) string {
	return fmt.Sprintf("users = [\n  %s,\n]\n", userEntry(ControlAdminUser, adminHash, []string{">"}, []string{">"}))
}

// agentPermissions are the subjects an agent may publish and subscribe to (§4a). It receives only
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
	// Presence: write its own record, read anyone's (design.md §7). A core publish needs no
	// JetStream API grant; the stream stores whatever arrives on its subjects.
	publish = append(publish, presenceSubject(token), presenceReadSubject("*"))
	for _, method := range agentBusdMethods {
		publish = append(publish, "busd."+method)
	}
	return publish, []string{"_INBOX." + token + ".>"}
}

// userEntry is one line of a users list. The line format is what userLine reads back.
func userEntry(user, passwordHash string, publish, subscribe []string) string {
	return fmt.Sprintf(`{user: %q, password: %q, permissions: {publish: {allow: %s}, subscribe: {allow: %s}}}`,
		user, passwordHash, quoteList(publish), quoteList(subscribe))
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
