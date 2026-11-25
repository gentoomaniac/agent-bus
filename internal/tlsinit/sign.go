package tlsinit

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// signatureAlgorithm is how data is signed with a key of this type: ECDSA and RSA (PKCS#1 v1.5)
// sign the SHA-256 digest, Ed25519 signs the data itself. It is the CA key's own algorithm, so
// whoever holds ca.pem can check what the CA key signed.
func signatureAlgorithm(public crypto.PublicKey) (x509.SignatureAlgorithm, error) {
	switch public.(type) {
	case *ecdsa.PublicKey:
		return x509.ECDSAWithSHA256, nil
	case *rsa.PublicKey:
		return x509.SHA256WithRSA, nil
	case ed25519.PublicKey:
		return x509.PureEd25519, nil
	}
	return 0, fmt.Errorf("a CA key of type %T cannot sign data", public)
}

// SignData signs data with the CA key, for what the agents verify against ca.pem alone (busd's
// directory). The caller names what the data is by prefixing a domain, so a signature made for
// one purpose is not valid for another.
func (ca *CA) SignData(data []byte) ([]byte, error) {
	algorithm, err := signatureAlgorithm(ca.key.Public())
	if err != nil {
		return nil, err
	}
	if algorithm == x509.PureEd25519 {
		return ca.key.Sign(rand.Reader, data, crypto.Hash(0))
	}
	digest := sha256.Sum256(data)
	return ca.key.Sign(rand.Reader, digest[:], crypto.SHA256)
}

// CheckDataSignature says whether signature is the CA's signature over data, by the CA certificate
// in caPEM.
func CheckDataSignature(caPEM, data, signature []byte) error {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return errors.New("the CA certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("reading the CA certificate: %w", err)
	}
	algorithm, err := signatureAlgorithm(certificate.PublicKey)
	if err != nil {
		return err
	}
	return certificate.CheckSignature(algorithm, data, signature)
}
