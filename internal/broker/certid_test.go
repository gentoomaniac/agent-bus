package broker

import (
	"slices"
	"strings"
	"testing"
)

func TestACertificateEntryNamesItsCertID(t *testing.T) {
	table := userTable{
		{role: roleAgent, name: "lib-x_bob", mode: modeCert, certID: "0123456789abcdef"},
		{role: roleAgent, name: "lib-x_carol", mode: modeCert}, // written before certIDs
		{role: roleAdmin, name: BusAdminUser, mode: modeCert, certID: "fedcba9876543210"},
	}
	body := table.render(nil)
	for _, want := range []string{`user: "agentbus:agent/lib-x_bob/0123456789abcdef"`, `user: "agentbus:agent/lib-x_carol"`, `user: "agentbus:admin/bus-admin/fedcba9876543210"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the file lacks %s:\n%s", want, body)
		}
	}
	parsed, err := parseUserTable(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range table {
		if index, found := parsed.find(user.role, user.name); !found || parsed[index] != user {
			t.Errorf("%s/%s read back as %+v", user.role, user.name, parsed)
		}
	}
}

func TestReplacingTheCertIDLeavesOnlyTheNewIdentity(t *testing.T) {
	table := userTable{{role: roleAgent, name: "lib-x_bob", mode: modeCert, certID: "aaaaaaaaaaaaaaaa"}}
	table.set(loginOf(roleAgent, "lib-x_bob", "", "bbbbbbbbbbbbbbbb"))
	body := table.render(nil)
	if strings.Contains(body, "aaaaaaaaaaaaaaaa") || !strings.Contains(body, "agentbus:agent/lib-x_bob/bbbbbbbbbbbbbbbb") {
		t.Errorf("after the replacement:\n%s", body)
	}
	table.set(loginOf(roleAgent, "lib-x_bob", "$2a$10$x", ""))
	if body := table.render(nil); strings.Contains(body, "agentbus:") {
		t.Errorf("a switch back to the password leaves a certificate identity:\n%s", body)
	}
}

func TestCertIDsDiffer(t *testing.T) {
	ids := []string{NewCertID(), NewCertID(), NewCertID()}
	slices.Sort(ids)
	if len(slices.Compact(ids)) != 3 || len(ids[0]) != 16 {
		t.Errorf("cert IDs %v", ids)
	}
}
