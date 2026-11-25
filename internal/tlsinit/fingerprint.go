package tlsinit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Fingerprint is the SHA-256 of a certificate's DER, as uppercase hex pairs separated by colons
// (what `openssl x509 -fingerprint -sha256` prints). The owner reads it off the central machine
// and a new machine checks the CA against it.
func Fingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(pairs, ":")
}

// FingerprintOfPEM is Fingerprint of the first certificate of a PEM file.
func FingerprintOfPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("no PEM certificate")
	}
	return Fingerprint(block.Bytes), nil
}

// SameFingerprint compares two fingerprints in any case, with or without colons or spaces.
func SameFingerprint(a, b string) bool {
	normalise := func(fingerprint string) string {
		return strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(fingerprint))
	}
	decoded, err := hex.DecodeString(normalise(a))
	other, otherErr := hex.DecodeString(normalise(b))
	return err == nil && otherErr == nil && len(decoded) == sha256.Size && string(decoded) == string(other)
}
