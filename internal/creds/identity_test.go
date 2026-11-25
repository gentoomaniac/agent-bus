package creds

import (
	"path/filepath"
	"testing"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/testcert"
)

func TestMatchesIdentityTakesTheIdentityAndItsCertIDsOnly(t *testing.T) {
	const identity = "agentbus:agent/lib-x_bob"
	for uri, want := range map[string]bool{
		identity:                       true, // made before certIDs
		identity + "/0123456789abcdef": true,
		identity + "/":                 false,
		identity + "x":                 false,
		"agentbus:agent/lib-x_bobby":   false,
		"agentbus:agent/lib-x_carol":   false,
		"":                             false,
	} {
		if got := MatchesIdentity(uri, identity); got != want {
			t.Errorf("MatchesIdentity(%q) = %v, want %v", uri, got, want)
		}
	}
	if CertID(identity+"/abc", identity) != "abc" || CertID(identity, identity) != "" {
		t.Error("CertID does not split the URI")
	}
}

func TestClientCertIdentityReadsTheURI(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testcert.Pair(t, "agentbus:agent/lib-x_bob/abc", time.Hour)
	if err := WriteClientCert(dir, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := ClientCertPaths(dir)
	if uri, err := ClientCertIdentity(certPath); err != nil || uri != "agentbus:agent/lib-x_bob/abc" {
		t.Errorf("ClientCertIdentity = %q, %v", uri, err)
	}
	if err := ValidateClientCert(certPath, keyPath, "agentbus:agent/lib-x_bob"); err != nil {
		t.Error(err)
	}
	if err := ValidateClientCert(certPath, keyPath, "agentbus:agent/lib-x_carol"); err == nil {
		t.Error("a certificate for bob passed as carol's")
	}
	if _, err := ClientCertIdentity(filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("a missing file has an identity")
	}
}
