// Package tlsinit creates the bus's private CA and the broker's certificate (docs/design/bus-model.md).
package tlsinit

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const caValidity = 10 * 365 * 24 * time.Hour

// Options say where the files go and what the broker certificate covers.
type Options struct {
	CACertPath, CAKeyPath         string
	BrokerCertPath, BrokerKeyPath string
	// SANs are DNS names or IP addresses; the first one is also the certificate's common name.
	SANs           []string
	BrokerValidity time.Duration
	Overwrite      bool
}

// DefaultSANs covers connections from the broker's own machine under its usual names.
func DefaultSANs() []string {
	sans := []string{"localhost", "127.0.0.1", "::1"}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		sans = append(sans, hostname)
	}
	return sans
}

// Create writes a new CA and a broker certificate signed by it.
func Create(opts Options) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "agent-bus CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	brokerPEM, brokerKeyPEM, err := issueBroker(caCert, caKey, caPEM, opts.SANs, opts.BrokerValidity)
	if err != nil {
		return err
	}

	outputs := []struct {
		path string
		pem  []byte
		mode os.FileMode
	}{
		{opts.CACertPath, caPEM, files.PublicMode},
		{opts.CAKeyPath, encodeKey(caKey), files.SecretMode},
		{opts.BrokerCertPath, brokerPEM, files.PublicMode},
		{opts.BrokerKeyPath, brokerKeyPEM, files.SecretMode},
	}
	for _, output := range outputs {
		if err := files.Write(output.path, output.pem, output.mode, opts.Overwrite); err != nil {
			return err
		}
	}
	return nil
}

// RenewBroker replaces the broker's certificate and key with new ones for sans, signed by the
// existing CA. Clients keep trusting the broker: they hold the CA, which does not change.
func RenewBroker(opts Options) error {
	ca, err := LoadCA(opts.CACertPath, opts.CAKeyPath)
	if err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
	brokerPEM, brokerKeyPEM, err := issueBroker(ca.cert, ca.key, caPEM, opts.SANs, opts.BrokerValidity)
	if err != nil {
		return err
	}
	// nats-server reads both at its next start; until then it keeps serving the old pair.
	if err := files.Write(opts.BrokerKeyPath, brokerKeyPEM, files.SecretMode, true); err != nil {
		return err
	}
	return files.Write(opts.BrokerCertPath, brokerPEM, files.PublicMode, true)
}

// issueBroker makes the broker's key and its certificate for sans, signed by the CA. The
// certificate PEM is followed by the CA's (see below).
func issueBroker(caCert *x509.Certificate, caKey crypto.Signer, caPEM []byte, sans []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	brokerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	brokerTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: sans[0]},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			brokerTemplate.IPAddresses = append(brokerTemplate.IPAddresses, ip)
		} else {
			brokerTemplate.DNSNames = append(brokerTemplate.DNSNames, san)
		}
	}
	brokerDER, err := x509.CreateCertificate(rand.Reader, brokerTemplate, caCert, &brokerKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	// The broker sends its certificate followed by the CA's, so that a new machine that was given
	// the CA's fingerprint can fetch the CA and check it (broker.FetchCA). Clients still trust
	// only the CA certificate they hold.
	certPEM = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: brokerDER}), caPEM...)
	return certPEM, encodeKey(brokerKey), nil
}

func encodeKey(key *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err) // only fails for unsupported curves
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func newSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // the system's random source is broken
	}
	return serial
}
