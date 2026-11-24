// Package identity holds an agent's Ed25519 key pair (design.md §6) and the lock that gives one
// session at a time the right to use it (§3).
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	privateKeyFile = "key"
	lockFile       = "lock"
)

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

// ReadPrivateKey loads the key WritePrivateKey stored in dir/key.
func ReadPrivateKey(dir string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(filepath.Join(dir, privateKeyFile))
	if err != nil {
		return nil, fmt.Errorf("reading the private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the private key file holds no PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an Ed25519 key")
	}
	return private, nil
}

// Lock is the claim of one adapter on an identity (§3). The operating system drops it when the
// process exits or crashes, so there is nothing stale to clean up.
type Lock struct{ file *os.File }

// HeldError says that another process holds the identity.
type HeldError struct{ HolderPID string }

func (err HeldError) Error() string {
	return "the identity is in use by another session (adapter process " + err.HolderPID + ")"
}

// AcquireLock takes an exclusive flock on dir/lock and writes the process ID into it. If another
// process holds it, the error is a HeldError naming that process.
func AcquireLock(dir string) (*Lock, error) {
	file, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(file.Name())
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, HeldError{HolderPID: strings.TrimSpace(string(holder))}
		}
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		file.Close()
		return nil, err
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		file.Close()
		return nil, err
	}
	return &Lock{file: file}, nil
}

// Release gives the identity up before the process ends.
func (lock *Lock) Release() error { return lock.file.Close() }
