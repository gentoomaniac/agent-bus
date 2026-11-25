package main

import (
	"os"
	"path/filepath"
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

// A staged admin pair becomes the login only once the server's file maps the certificate, so an
// interrupted `cert issue --admin` can be retried or rolled back whichever way it stopped.
func TestStagedAdminCertIsActivatedOnlyWhenTheServerMapsIt(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&AdminInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	dir := creds.AdminDir(cli.ConfigDir, broker.BusAdminUser)
	if err := creds.StageClientCert(dir, []byte("c"), []byte("k")); err != nil {
		t.Fatal(err)
	}

	if err := cli.activateStagedAdminCerts(); err != nil {
		t.Fatal(err)
	}
	if has, _ := creds.HasClientCert(dir); has || !creds.HasStagedClientCert(dir) {
		t.Fatal("the pair was activated although the server still wants the password")
	}

	mapped := `users = [
  {user: "agentbus:admin/bus-admin", permissions: {publish: {allow: [">"]}, subscribe: {allow: [">"]}}, allowed_connection_types: ["WEBSOCKET"]}, # admin
]
`
	if err := os.WriteFile(filepath.Join(cli.ConfigDir, "nats", "auth.conf"), []byte(mapped), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cli.activateStagedAdminCerts(); err != nil {
		t.Fatal(err)
	}
	if has, err := creds.HasClientCert(dir); !has || err != nil || creds.HasStagedClientCert(dir) {
		t.Errorf("after the server maps it: has %v, err %v", has, err)
	}
}
