// Package creds stores passwords in the per-principal directories of the config directory.
// The user name is not stored: it is the admin user's name for an admin and the agent token for
// an agent.
package creds

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	passwordFile        = "password"
	passwordBytes       = 32
	monitorPasswordFile = "monitor/password.bcrypt"
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

// WriteMonitorPassword stores the bcrypt hash of the web monitor's password, replacing the old one.
func WriteMonitorPassword(configDir, password string) error {
	if len(password) < MinMonitorPasswordBytes {
		return fmt.Errorf("the password must have at least %d characters", MinMonitorPasswordBytes)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err // for example longer than bcrypt's 72 bytes
	}
	return files.Write(filepath.Join(configDir, monitorPasswordFile), append(hash, '\n'), files.SecretMode, true)
}

// ReadMonitorPasswordHash returns the stored hash, or nil when no password is set. A file with
// no hash in it is an error, not "no password": it must not leave the monitor open.
func ReadMonitorPasswordHash(configDir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(configDir, monitorPasswordFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the monitor password: %w", err)
	}
	hash := strings.TrimSpace(string(data))
	if hash == "" {
		return nil, errors.New("the monitor password file is empty: set a password again (busctl monitor password) or remove the file")
	}
	return []byte(hash), nil
}
