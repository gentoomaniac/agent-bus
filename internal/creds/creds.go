// Package creds stores passwords and client certificates in the per-principal directories of the config directory.
// The user name is not stored: it is the admin user's name for an admin and the agent token for
// an agent.
package creds

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	passwordFile        = "password"
	clientCertFile      = "client.pem"
	clientKeyFile       = "client.key"
	passwordBytes       = 32
	monitorPasswordFile = "monitor-web/password.bcrypt"
	// oldMonitorPasswordFile was easily confused with the monitor@<owner> agent folder.
	oldMonitorPasswordFile = "monitor/password.bcrypt"
	// MinMonitorPasswordBytes keeps out the passwords that are guessed in a few tries.
	MinMonitorPasswordBytes = 8
)

// NewPassword returns a random password that is safe in URLs, shell words and `user:password` pairs.
func NewPassword() string {
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // the system's random source is broken
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// AdminDir holds the credentials of one admin user (bus-admin or control-admin).
func AdminDir(configDir, adminUser string) string {
	return filepath.Join(configDir, adminUser)
}

// AgentDir holds an agent's private key and credentials.
func AgentDir(configDir, agentID string) string {
	return filepath.Join(configDir, agentID)
}

// ClientCertPaths are the certificate and key of a certificate login inside a credentials directory.
func ClientCertPaths(dir string) (certPath, keyPath string) {
	return filepath.Join(dir, clientCertFile), filepath.Join(dir, clientKeyFile)
}

// HasClientCert says whether dir holds a certificate login, which clients prefer to a password.
func HasClientCert(dir string) bool {
	certPath, keyPath := ClientCertPaths(dir)
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	return certErr == nil && keyErr == nil
}

// WriteClientCert stores a certificate and its key, replacing earlier ones (renewal).
func WriteClientCert(dir string, certPEM, keyPEM []byte) error {
	certPath, keyPath := ClientCertPaths(dir)
	if err := files.Write(keyPath, keyPEM, files.SecretMode, true); err != nil {
		return err
	}
	return files.Write(certPath, certPEM, files.PublicMode, true)
}

// RemoveClientCert deletes the certificate login of dir, so clients use the password again.
func RemoveClientCert(dir string) error {
	certPath, keyPath := ClientCertPaths(dir)
	for _, path := range []string{certPath, keyPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// WritePassword stores the password in dir/password with mode 0600.
func WritePassword(dir, password string, overwrite bool) error {
	return files.Write(filepath.Join(dir, passwordFile), []byte(password+"\n"), files.SecretMode, overwrite)
}

// ReadPassword returns the password stored in dir/password.
func ReadPassword(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, passwordFile))
	if err != nil {
		return "", fmt.Errorf("reading credentials: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// MonitorPasswordPath is the file that holds the web monitor's password hash.
func MonitorPasswordPath(configDir string) string {
	return filepath.Join(configDir, monitorPasswordFile)
}

// WriteMonitorPassword stores the bcrypt hash of the web monitor's password, replacing the old one.
func WriteMonitorPassword(configDir, password string) error {
	if len(password) < MinMonitorPasswordBytes {
		return fmt.Errorf("the password must have at least %d characters", MinMonitorPasswordBytes)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err // for example longer than bcrypt's 72 bytes
	}
	return files.Write(MonitorPasswordPath(configDir), append(hash, '\n'), files.SecretMode, true)
}

// ReadMonitorPasswordHash returns the stored hash, or nil when no password is set. A file with
// no hash in it is an error, not "no password": it must not leave the monitor open. The old
// location is read when the new one does not exist yet, so that existing setups keep working.
func ReadMonitorPasswordHash(configDir string) ([]byte, error) {
	data, err := os.ReadFile(MonitorPasswordPath(configDir))
	if errors.Is(err, os.ErrNotExist) {
		oldPath := filepath.Join(configDir, oldMonitorPasswordFile)
		if data, err = os.ReadFile(oldPath); err == nil {
			log.Printf("the monitor password moved from %s to %s: it is read from the old place until "+
				"`agent-busctl monitor password` writes the new file", oldPath, MonitorPasswordPath(configDir))
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the monitor password: %w", err)
	}
	hash := strings.TrimSpace(string(data))
	if hash == "" {
		return nil, errors.New("the monitor password file is empty: set a password again (agent-busctl monitor password) or remove the file")
	}
	return []byte(hash), nil
}
