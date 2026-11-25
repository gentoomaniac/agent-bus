package tlsinit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"net"
	"net/url"
	"testing"
	"time"
)

const testIdentity = "agentbus:agent/lib-x_bob"

func testCA(t *testing.T) (*CA, *x509.CertPool) {
	t.Helper()
	opts := testOptions(t.TempDir())
	if err := Create(opts); err != nil {
		t.Fatal(err)
	}
	ca, err := LoadCA(opts.CACertPath, opts.CAKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	return ca, roots
}

func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestIssueClientCertHasOnlyTheApprovedIdentity(t *testing.T) {
	ca, roots := testCA(t)
	keyPEM, public, err := NewClientKey()
	if err != nil || len(keyPEM) == 0 {
		t.Fatal(err)
	}
	certPEM, err := ca.IssueClientCert(testIdentity, public, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, certPEM)

	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("not valid as a client certificate: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Error("also valid as a server certificate")
	}
	if cert.IsCA {
		t.Error("the certificate is a CA")
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != testIdentity {
		t.Errorf("URIs = %v, want only %s", cert.URIs, testIdentity)
	}
	if len(cert.DNSNames)+len(cert.EmailAddresses)+len(cert.IPAddresses) != 0 {
		t.Errorf("unexpected names: %v %v %v", cert.DNSNames, cert.EmailAddresses, cert.IPAddresses)
	}
	if got := cert.NotAfter.Sub(cert.NotBefore); got > 2*time.Hour+2*time.Minute || got < 2*time.Hour {
		t.Errorf("validity %v, want about 2h", got)
	}
}

func TestSignCSRUsesOnlyTheKey(t *testing.T) {
	ca, _ := testCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// A request that asks for an admin identity, other names and a CA extension.
	basicConstraintsCA, _ := asn1.Marshal(struct {
		IsCA bool `asn1:"optional"`
	}{true})
	adminURI, _ := url.Parse("agentbus:admin/bus-admin")
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:         pkix.Name{CommonName: "agentbus:admin/bus-admin"},
		DNSNames:        []string{"broker.lan"},
		IPAddresses:     []net.IP{net.ParseIP("10.0.0.1")},
		EmailAddresses:  []string{"root@example.org"},
		URIs:            []*url.URL{adminURI},
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: basicConstraintsCA}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	certPEM, err := ca.SignCSR(testIdentity, csrPEM, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, certPEM)
	if cert.IsCA || len(cert.URIs) != 1 || cert.URIs[0].String() != testIdentity ||
		len(cert.DNSNames)+len(cert.EmailAddresses)+len(cert.IPAddresses) != 0 || cert.Subject.CommonName != testIdentity {
		t.Errorf("the certificate took something from the request: %+v", cert)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		t.Error("the certificate is not for the request's key")
	}
}

func TestSignCSRRefusesWhatIsNotProofOfTheKey(t *testing.T) {
	ca, _ := testCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), csrDER...)
	tampered[len(tampered)-1] ^= 0xff // the last byte is part of the signature

	for name, csrPEM := range map[string][]byte{
		"tampered signature": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered}),
		"not PEM":            []byte("hello"),
		"wrong PEM type":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: csrDER}),
	} {
		if _, err := ca.SignCSR(testIdentity, csrPEM, time.Hour); err == nil {
			t.Errorf("%s: signed without an error", name)
		}
	}
}

func TestIssueClientCertRefusesBadIdentities(t *testing.T) {
	ca, _ := testCA(t)
	_, public, _ := NewClientKey()
	for _, identity := range []string{"", "lib-x_bob", "agentbus:", "://x"} {
		if _, err := ca.IssueClientCert(identity, public, time.Hour); err == nil {
			t.Errorf("identity %q accepted", identity)
		}
	}
}

func TestIssueClientCertRefusesBadValidityAndStopsAtTheCA(t *testing.T) {
	ca, _ := testCA(t)
	_, public, _ := NewClientKey()
	for _, validity := range []time.Duration{0, -time.Hour} {
		if _, err := ca.IssueClientCert(testIdentity, public, validity); err == nil {
			t.Errorf("validity %s was accepted", validity)
		}
	}

	certPEM, err := ca.IssueClientCert(testIdentity, public, 250*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseCertPEM(t, certPEM).NotAfter; got.After(ca.cert.NotAfter) {
		t.Errorf("NotAfter %s is after the CA's %s", got, ca.cert.NotAfter)
	}

	expired := *ca
	expiredCert := *ca.cert
	expiredCert.NotAfter = time.Now().Add(-time.Hour)
	expired.cert = &expiredCert
	if _, err := expired.IssueClientCert(testIdentity, public, time.Hour); err == nil {
		t.Error("an expired CA issued a certificate")
	}
}
