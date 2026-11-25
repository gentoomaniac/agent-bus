package broker

import (
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/testcert"
)

func TestMappedAddressKeepsTheHost(t *testing.T) {
	for address, want := range map[string]string{
		"bus.lan:4222": "wss://bus.lan:4223",
		"[::1]:4222":   "wss://[::1]:4223",
	} {
		if got, err := MappedAddress(address, 4223); err != nil || got != want {
			t.Errorf("MappedAddress(%q) = %q, %v; want %q", address, got, err, want)
		}
	}
	if _, err := MappedAddress("no-port", 4223); err == nil {
		t.Error("an address without a port was accepted")
	}
}

func TestUserConnectionPrefersTheCertificate(t *testing.T) {
	dir := t.TempDir()
	if err := creds.WritePassword(dir, "secret", false); err != nil {
		t.Fatal(err)
	}
	conn, err := UserConnection(dir, "lib-x_bob", "bus.lan:4222", 4223, "ca.pem")
	if err != nil || conn.Password != "secret" || conn.ClientCertPath != "" || conn.Address != "bus.lan:4222" {
		t.Errorf("without certificate files: %+v, %v", conn, err)
	}

	certPEM, keyPEM := testcert.Pair(t, certIdentity(roleAgent, "lib-x_bob"), time.Hour)
	if err := creds.WriteClientCert(dir, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	conn, err = UserConnection(dir, "lib-x_bob", "bus.lan:4222", 4223, "ca.pem")
	if err != nil || conn.Password != "" || conn.ClientCertPath == "" || conn.Address != "wss://bus.lan:4223" {
		t.Errorf("with certificate files: %+v, %v", conn, err)
	}

	otherPEM, otherKey := testcert.Pair(t, certIdentity(roleAgent, "lib-x_carol"), time.Hour)
	if err := creds.WriteClientCert(dir, otherPEM, otherKey); err != nil {
		t.Fatal(err)
	}
	if _, err := UserConnection(dir, "lib-x_bob", "bus.lan:4222", 4223, "ca.pem"); err == nil {
		t.Error("another agent's certificate was used")
	}
}
