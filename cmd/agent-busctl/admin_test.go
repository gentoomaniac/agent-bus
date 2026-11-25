package main

import (
	"testing"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

func TestAdminInitForceRetiresTheAdminCertificates(t *testing.T) {
	cli := &CLI{ConfigDir: t.TempDir()}
	if err := (&AdminInitCmd{}).Run(cli); err != nil {
		t.Fatal(err)
	}
	for _, user := range adminUsers {
		if err := creds.WriteClientCert(creds.AdminDir(cli.ConfigDir, user), []byte("c"), []byte("k")); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&AdminInitCmd{Force: true}).Run(cli); err != nil {
		t.Fatal(err)
	}
	for _, user := range adminUsers {
		if has, err := creds.HasClientCert(creds.AdminDir(cli.ConfigDir, user)); has || err != nil {
			t.Errorf("%s still has a certificate login: %v, %v", user, has, err)
		}
	}
}
