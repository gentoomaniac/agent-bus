// Package creds stores passwords in the per-principal directories of the config directory.
// The user name is not stored: it is the admin user's name for an admin and the agent token for
// an agent.
package creds

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	passwordFile  = "password"
	passwordBytes = 32
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
