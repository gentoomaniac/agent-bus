// Package tlsinit creates the bus's private CA and the broker's certificate (design.md §4).
package tlsinit

import (
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

	brokerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	brokerTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: opts.SANs[0]},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(opts.BrokerValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, san := range opts.SANs {
		if ip := net.ParseIP(san); ip != nil {
			brokerTemplate.IPAddresses = append(brokerTemplate.IPAddresses, ip)
		} else {
			brokerTemplate.DNSNames = append(brokerTemplate.DNSNames, san)
		}
	}
	brokerDER, err := x509.CreateCertificate(rand.Reader, brokerTemplate, caCert, &brokerKey.PublicKey, caKey)
	if err != nil {
		return err
	}

	outputs := []struct {
		path string
		pem  []byte
		mode os.FileMode
	}{
		{opts.CACertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), files.PublicMode},
		{opts.CAKeyPath, encodeKey(caKey), files.SecretMode},
		{opts.BrokerCertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: brokerDER}), files.PublicMode},
		{opts.BrokerKeyPath, encodeKey(brokerKey), files.SecretMode},
	}
	for _, output := range outputs {
		if err := files.Write(output.path, output.pem, output.mode, opts.Overwrite); err != nil {
			return err
		}
	}
	return nil
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
