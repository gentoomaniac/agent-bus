package broker

import (
	"strings"
	"testing"
)

func TestServicePrincipalsHaveNarrowGrants(t *testing.T) {
	entry := func(role authRole, user string) string {
		return userEntry(authUser{role: role, name: user, mode: modePassword, passwordHash: "$2a$10$x"}, nil)
	}
	entries := map[string]string{
		BusdUser:           entry(roleBusd, BusdUser),
		MonitorBusUser:     entry(roleMonitor, MonitorBusUser),
		MonitorControlUser: entry(roleMonitor, MonitorControlUser),
	}
	for user, entry := range entries {
		for _, forbidden := range []string{`">"`, "STREAM.DELETE", "STREAM.PURGE", "STREAM.CREATE", "STREAM.UPDATE", "$JS.API.>", "CONSUMER.DURABLE", "$SYS"} {
			if strings.Contains(entry, forbidden) {
				t.Errorf("%s may use %s: %s", user, forbidden, entry)
			}
		}
	}
	if !strings.Contains(entries[BusdUser], `"$KV.registry.>"`) {
		t.Error("busd cannot write registry records")
	}
	for _, user := range []string{MonitorBusUser, MonitorControlUser} {
		if strings.Contains(entries[user], "$KV") || strings.Contains(entries[user], "dm.") || strings.Contains(entries[user], "busd.ca") {
			t.Errorf("the monitor login %s can write: %s", user, entries[user])
		}
	}
	if !strings.Contains(entries[MonitorBusUser], `"$JS.API.STREAM.MSG.GET.BUS"`) || strings.Contains(entries[MonitorBusUser], "DIRECT.GET.BUS") {
		t.Errorf("the monitor reads a page with STREAM.MSG.GET.BUS and no direct get: %s", entries[MonitorBusUser])
	}
	if !strings.Contains(entries[MonitorControlUser], `subscribe: {allow: ["_INBOX.bus-monitor.>", "busd.enroll.pending"]}`) {
		t.Errorf("the control monitor hears more than the pending count: %s", entries[MonitorControlUser])
	}
	if strings.Contains(entries[MonitorControlUser], "$JS") {
		t.Errorf("the control monitor has raw JetStream access: %s", entries[MonitorControlUser])
	}
	if unknown := entry(roleMonitor, "legacy"); !strings.Contains(unknown, `publish: {deny: [">"]}`) {
		t.Errorf("an unknown monitor entry has access: %s", unknown)
	}
}
