// Package identity creates an agent's Ed25519 key pair (design.md §6).
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"path/filepath"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const privateKeyFile = "key"

// Generate creates a new key pair.
func Generate() (ed25519.PublicKey, ed25519.PrivateKey) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err) // the system's random source is broken
	}
	return public, private
}

// Fingerprint is "SHA256:" and the unpadded base64 of the SHA-256 of the raw public key, like SSH.
func Fingerprint(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// WritePrivateKey stores the key as PKCS#8 PEM in dir/key with mode 0600. It never overwrites a key.
func WritePrivateKey(dir string, private ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return files.Write(filepath.Join(dir, privateKeyFile), pemBytes, files.SecretMode, false)
}
