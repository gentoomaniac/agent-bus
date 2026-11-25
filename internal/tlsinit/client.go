package tlsinit

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// DefaultClientValidity is how long a client certificate lasts unless the owner says otherwise.
const DefaultClientValidity = 365 * 24 * time.Hour

const minRSABits = 2048

// CA is the bus's certificate authority with its private key: what signs client certificates.
type CA struct {
	cert *x509.Certificate
	key  crypto.Signer
}

// LoadCA reads the CA certificate and key written by Create.
func LoadCA(certPath, keyPath string) (*CA, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading the CA (agent-busctl tls init creates it): %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%s is not a CA certificate", certPath)
	}
	return &CA{cert: cert, key: pair.PrivateKey.(crypto.Signer)}, nil
}

// IssueClientCert signs a certificate for the public key that is good for one thing: logging in
// as identity. Everything in it comes from the arguments, never from whoever asked for it, so
// the holder of the key cannot add names, usages or CA rights. identity is the URI SAN
// nats-server maps to a user (see broker.certIdentity); it is also the common name.
func (ca *CA) IssueClientCert(identity string, publicKey crypto.PublicKey, validity time.Duration) ([]byte, error) {
	uri, err := url.Parse(identity)
	if err != nil || uri.Scheme == "" || uri.Opaque == "" {
		return nil, fmt.Errorf("%q is not a URI of the form scheme:path", identity)
	}
	if rsaKey, isRSA := publicKey.(*rsa.PublicKey); isRSA && rsaKey.N.BitLen() < minRSABits {
		return nil, fmt.Errorf("the RSA key has %d bits, at least %d are required", rsaKey.N.BitLen(), minRSABits)
	}
	now := time.Now()
	if validity <= 0 {
		return nil, fmt.Errorf("the validity must be positive, got %s", validity)
	}
	if now.Before(ca.cert.NotBefore) || now.After(ca.cert.NotAfter) {
		return nil, fmt.Errorf("the CA is not valid now (%s to %s)", ca.cert.NotBefore.Format(time.DateOnly), ca.cert.NotAfter.Format(time.DateOnly))
	}
	// A certificate that outlives its CA would stop working with the CA, whatever it says.
	notAfter := now.Add(validity)
	if notAfter.After(ca.cert.NotAfter) {
		notAfter = ca.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: identity},
		URIs:                  []*url.URL{uri},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, publicKey, ca.key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// SignCSR issues a client certificate for the key of a certificate request, after checking that
// the requester holds the key. Nothing else of the request is used.
func (ca *CA) SignCSR(identity string, csrPEM []byte, validity time.Duration) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("not a PEM certificate request")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := request.CheckSignature(); err != nil {
		return nil, fmt.Errorf("the request's signature is invalid: %w", err)
	}
	return ca.IssueClientCert(identity, request.PublicKey, validity)
}

// NewClientKey generates a key for a client certificate, as PEM for a file and as a key to be
// certified.
func NewClientKey() (keyPEM []byte, public crypto.PublicKey, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return encodeKey(key), &key.PublicKey, nil
}
