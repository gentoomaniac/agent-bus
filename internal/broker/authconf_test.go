package broker

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"golang.org/x/crypto/bcrypt"
)

func TestUserTableRoundTripsEveryRoleAndMode(t *testing.T) {
	adminHash, err := HashPassword("admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	aliceHash, _ := HashPassword("alice-secret")
	table := userTable{
		{role: roleAdmin, name: BusAdminUser, mode: modePassword, passwordHash: adminHash},
		{role: roleAdmin, name: ControlAdminUser, mode: modeCert},
		{role: roleAgent, name: "api-client_alice", mode: modePassword, passwordHash: aliceHash},
		{role: roleAgent, name: "lib-x_bob", mode: modeCert},
		{role: roleMonitor, name: "monitor", mode: modeCert},
		{role: roleMonitor, name: "legacy-monitor", mode: modePassword, passwordHash: "$2a$10$m"},
		{role: roleEnrollment, name: "enroll", mode: modePassword, passwordHash: "$2a$10$e"},
		{role: roleEnrollment, name: "enroll-cert", mode: modeCert},
	}

	body := table.render([]string{"lib-x", "release"})
	parsed, err := parseUserTable(body)
	if err != nil {
		t.Fatal(err)
	}
	sortedWant := slices.Clone(table)
	slices.SortFunc(sortedWant, func(a, b authUser) int { return strings.Compare(string(a.role)+a.name, string(b.role)+b.name) })
	if !slices.Equal(parsed, sortedWant) {
		t.Errorf("parse(render(table)) = %+v, want %+v", parsed, sortedWant)
	}
	if again := parsed.render([]string{"lib-x", "release"}); again != body {
		t.Errorf("render is not stable:\n%s\nvs\n%s", body, again)
	}
	if bcrypt.CompareHashAndPassword([]byte(parsed[2].passwordHash), []byte("alice-secret")) != nil {
		t.Error("the hash read back no longer matches the password")
	}
	for _, want := range []string{`user: "agentbus:agent/lib-x_bob"`, `allowed_connection_types: ["WEBSOCKET"]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s", want)
		}
	}
}

func TestParseUserTableReadsAFileWrittenBeforeRoles(t *testing.T) {
	old := `users = [
  {user: "bus-admin", password: "$2a$10$a", permissions: {publish: {allow: [">"]}, subscribe: {allow: [">"]}}},
  {user: "lib-x_bob", password: "$2a$10$b", permissions: {publish: {allow: ["dm.*"]}, subscribe: {allow: ["_INBOX.lib-x_bob.>"]}}},
]
`
	table, err := parseUserTable(old)
	if err != nil {
		t.Fatal(err)
	}
	want := userTable{
		{role: roleAdmin, name: BusAdminUser, mode: modePassword, passwordHash: "$2a$10$a"},
		{role: roleAgent, name: "lib-x_bob", mode: modePassword, passwordHash: "$2a$10$b"},
	}
	if !slices.Equal(table, want) {
		t.Errorf("parsed %+v, want %+v", table, want)
	}
}

func TestParseUserTableRefusesWhatItCannotKeep(t *testing.T) {
	for name, body := range map[string]string{
		"unknown role":              `  {user: "a_b", password: "h", permissions: {}}, # superuser`,
		"hand-written entry":        `  {user: "a_b", token: "t"}`,
		"certificate role mismatch": `  {user: "agentbus:admin/a_b", permissions: {}}, # agent`,
		"no login":                  `  {user: "a_b", permissions: {}}, # agent`,
	} {
		if _, err := parseUserTable("users = [\n" + body + "\n]\n"); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}

func TestUserTableGrantsPerAgent(t *testing.T) {
	table := userTable{{role: roleAgent, name: "lib-x_bob", mode: modeCert}}
	body := table.render([]string{"lib-x", "release"})

	for _, want := range []string{
		`"$JS.API.CONSUMER.MSG.NEXT.BUS.lib-x_bob__dm"`,
		`"$JS.API.CONSUMER.INFO.BUS.lib-x_bob__general"`,
		`"$JS.ACK.BUS.lib-x_bob__g__release.>"`,
		`"_INBOX.lib-x_bob.>"`,
		`"busd.history"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("auth body lacks grant %s", want)
		}
	}
	// An agent never gets another agent's consumer, a consumer management API, a direct get,
	// or any subject of CONTROL.
	publish, subscribe := agentPermissions("lib-x_bob", []string{"lib-x"})
	for _, subject := range append(publish, subscribe...) {
		if subject == presenceReadSubject("*") {
			continue // any agent's presence may be read: the one direct get an agent has
		}
		for _, forbidden := range []string{"alice", "CONSUMER.CREATE", "CONSUMER.DELETE", "DURABLE", "DIRECT", "$KV", "ledger", "private", "busd.>", "busd.agent.register"} {
			if strings.Contains(subject, forbidden) {
				t.Errorf("agent permission %q contains %q", subject, forbidden)
			}
		}
		if strings.HasSuffix(subject, "*") && subject != "dm.*" && subject != "topic.*" {
			t.Errorf("agent permission %q is a wildcard", subject)
		}
	}
}

func TestAgentMayPublishOnlyItsOwnPresenceAndReadAnyones(t *testing.T) {
	publish, subscribe := agentPermissions("lib-x_bob", nil)
	for _, want := range []string{"presence.lib-x_bob", presenceReadSubject("*")} {
		if !slices.Contains(publish, want) {
			t.Errorf("publish grants lack %q", want)
		}
	}
	for _, subject := range append(publish, subscribe...) {
		if strings.HasPrefix(subject, "presence.") && subject != "presence.lib-x_bob" {
			t.Errorf("grant %q reaches another agent's presence", subject)
		}
		if strings.Contains(subject, "DIRECT") && subject != presenceReadSubject("*") {
			t.Errorf("grant %q is a direct get outside PRESENCE", subject)
		}
	}
}

func TestTableSetReplacesAndRemoveDrops(t *testing.T) {
	table := userTable{{role: roleAgent, name: "a_b", mode: modePassword, passwordHash: "old"}}
	table.set(authUser{role: roleAgent, name: "a_b", mode: modeCert})
	table.set(authUser{role: roleAdmin, name: "a_b", mode: modePassword, passwordHash: "x"})
	if len(table) != 2 || table[0].mode != modeCert {
		t.Errorf("after set: %+v", table)
	}
	table.remove(roleAgent, "a_b")
	if len(table) != 1 || table[0].role != roleAdmin {
		t.Errorf("after remove: %+v", table)
	}
}

func TestWriteAdminAuthKeepsCertificateAgents(t *testing.T) {
	server := Server{Dir: t.TempDir()}
	if err := server.WriteAdminAuth("$2a$10$one", "$2a$10$two"); err != nil {
		t.Fatal(err)
	}
	body, _ := splitGeneration(readFile(t, server.path(authFile)))
	table, err := parseUserTable(body)
	if err != nil {
		t.Fatal(err)
	}
	table.set(authUser{role: roleAgent, name: "lib-x_bob", mode: modeCert})
	table.set(authUser{role: roleAgent, name: "lib-x_carol", mode: modePassword, passwordHash: "$2a$10$carol"})
	rendered := table.render([]string{"team"}) // the agents' grants include the group consumer
	if err := writeAuthFile(server.path(authFile), 5, rendered); err != nil {
		t.Fatal(err)
	}

	if err := server.WriteAdminAuth("$2a$10$three", "$2a$10$four"); err != nil {
		t.Fatal(err)
	}
	body, generation := splitGeneration(readFile(t, server.path(authFile)))
	table, err = parseUserTable(body)
	if err != nil || generation != 6 || len(table) != 3 {
		t.Fatalf("after the second init: %+v, generation %d, %v", table, generation, err)
	}
	if index, _ := table.find(roleAdmin, BusAdminUser); table[index].passwordHash != "$2a$10$three" {
		t.Errorf("admin hash not replaced: %+v", table)
	}
	if _, found := table.find(roleAgent, "lib-x_bob"); !found {
		t.Error("the certificate agent was lost")
	}
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "lib-x_") && !strings.Contains(body, line) {
			t.Errorf("an agent's entry changed, group grants lost?\n%s", line)
		}
	}
	if !strings.Contains(body, consumerName("lib-x_carol", bus.GroupChannel("team"))) {
		t.Error("carol lost her group consumer")
	}
}

func TestEnrollmentLoginMayOnlyRequestAndHearItsInbox(t *testing.T) {
	entry := userEntry(authUser{role: roleEnrollment, name: "enroll", mode: modePassword, passwordHash: "$2a$10$e"}, nil)
	want := `{publish: {allow: ["busd.enroll.request", "busd.enroll.status"]}, subscribe: {allow: ["_INBOX.enroll.>"]}}`
	if !strings.Contains(entry, want) {
		t.Errorf("enrollment entry %s lacks %s", entry, want)
	}
	for _, forbidden := range []string{"$JS", "dm.", "general", "ledger", "$KV", "busd.ca", "busd.>"} {
		if strings.Contains(entry, forbidden) {
			t.Errorf("enrollment entry mentions %q", forbidden)
		}
	}
}

func TestWriteAdminAuthKeepsTheEnrollmentLogin(t *testing.T) {
	server := Server{Dir: t.TempDir()}
	if err := server.WriteAdminAuth("$2a$10$one", "$2a$10$two"); err != nil {
		t.Fatal(err)
	}
	body, generation := splitGeneration(readFile(t, server.path(controlAuthFile)))
	table, err := parseUserTable(body)
	if err != nil {
		t.Fatal(err)
	}
	table.set(authUser{role: roleEnrollment, name: "enroll", mode: modePassword, passwordHash: "$2a$10$e"})
	if err := writeAuthFile(server.path(controlAuthFile), generation+1, table.render(nil)); err != nil {
		t.Fatal(err)
	}

	if err := server.WriteAdminAuth("$2a$10$three", "$2a$10$four"); err != nil {
		t.Fatal(err)
	}
	body, _ = splitGeneration(readFile(t, server.path(controlAuthFile)))
	table, err = parseUserTable(body)
	if err != nil || len(table) != 2 {
		t.Fatalf("control users after admin init --force: %+v, %v", table, err)
	}
	if index, _ := table.find(roleAdmin, ControlAdminUser); table[index].passwordHash != "$2a$10$four" {
		t.Error("the control admin's hash was not replaced")
	}
	if index, found := table.find(roleEnrollment, "enroll"); !found || table[index].passwordHash != "$2a$10$e" {
		t.Error("the enrollment login was lost")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestSplitGeneration(t *testing.T) {
	body, generation := splitGeneration("# generation 7\nusers = []\n")
	if body != "users = []\n" || generation != 7 {
		t.Errorf("splitGeneration = %q, %d", body, generation)
	}
	if body, generation := splitGeneration("users = []\n"); body != "users = []\n" || generation != 0 {
		t.Errorf("a file without header: %q, %d", body, generation)
	}
}
