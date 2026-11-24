package broker

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBusAuthRoundTripsAndGrantsPerAgent(t *testing.T) {
	adminHash, err := HashPassword("admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	aliceHash, _ := HashPassword("alice-secret")
	auth := busAuth{adminHash: adminHash, agentHashes: map[string]string{"api-client_alice": aliceHash, "lib-x_bob": "$2a$10$x"}}

	body := auth.render([]string{"lib-x", "release"})
	parsed := parseBusAuth(body)
	if parsed.adminHash != adminHash || parsed.agentHashes["api-client_alice"] != aliceHash || len(parsed.agentHashes) != 2 {
		t.Errorf("parse(render(auth)) = %+v, want the same hashes back", parsed)
	}
	if bcrypt.CompareHashAndPassword([]byte(parsed.agentHashes["api-client_alice"]), []byte("alice-secret")) != nil {
		t.Error("the hash read back no longer matches the password")
	}

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

func TestReplaceAdminHashKeepsTheAgents(t *testing.T) {
	auth := busAuth{adminHash: "$2a$10$old", agentHashes: map[string]string{"a_b": "$2a$10$agent"}}
	body := replaceAdminHash(auth.render([]string{"g"}), "$2a$10$new")
	parsed := parseBusAuth(body)
	if parsed.adminHash != "$2a$10$new" || parsed.agentHashes["a_b"] != "$2a$10$agent" {
		t.Errorf("after replacing the admin hash: %+v", parsed)
	}
	if !strings.Contains(body, "MSG.NEXT.BUS.a_b__g__g") {
		t.Error("the agent lost its group grants")
	}
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
