package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/broker"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/testcert"
)

// A staged admin pair becomes the login only once the server's file maps the certificate, so an
// interrupted `cert issue --admin` can be retried or rolled back whichever way it stopped.
func TestStagedAdminCertIsActivatedOnlyWhenTheServerMapsIt(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&AdminInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	dir := creds.AdminDir(cli.ConfigDir, broker.BusAdminUser)
	certPEM, keyPEM := testcert.Pair(t, broker.AdminCertIdentity(broker.BusAdminUser), time.Hour)
	if err := creds.StageClientCert(dir, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}

	if err := cli.activateStagedAdminCerts(); err != nil {
		t.Fatal(err)
	}
	has, _ := creds.HasClientCert(dir)
	staged, _ := creds.HasStagedClientCert(dir)
	if has || !staged {
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
	has, err := creds.HasClientCert(dir)
	staged, _ = creds.HasStagedClientCert(dir)
	if !has || err != nil || staged {
		t.Errorf("after the server maps it: has %v, err %v, staged %v", has, err, staged)
	}
}
