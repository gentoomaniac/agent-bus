// Package creds stores passwords and client certificates in the per-principal directories of the config directory.
// The user name is not stored: it is the admin user's name for an admin and the agent token for
// an agent.
package creds

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	passwordFile        = "password"
	clientCertFile      = "client.pem"
	clientKeyFile       = "client.key"
	stagedSuffix        = ".new"
	passwordBytes       = 32
	monitorPasswordFile = "monitor-web/password.bcrypt"
	monitorPrefsFile    = "prefs.json"
	// oldMonitorPasswordFile was easily confused with the monitor@<owner> agent folder.
	oldMonitorPasswordFile = "monitor/password.bcrypt"
	// MinMonitorPasswordBytes keeps out the passwords that are guessed in a few tries.
	MinMonitorPasswordBytes = 8
)

// NewPassword returns a random password that is safe in URLs, shell words and `user:password` pairs,
// and never starts with '-', which a command line would take for a flag.
func NewPassword() string {
	for {
		raw := make([]byte, passwordBytes)
		if _, err := rand.Read(raw); err != nil {
			panic(err) // the system's random source is broken
		}
		if password := base64.RawURLEncoding.EncodeToString(raw); password[0] != '-' {
			return password
		}
	}
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
// Half a pair, or a file that cannot be examined, is an error: falling back to the password would
// hide a broken login.
func HasClientCert(dir string) (bool, error) {
	certPath, keyPath := ClientCertPaths(dir)
	return hasPair(certPath, keyPath)
}

func hasPair(certPath, keyPath string) (bool, error) {
	hasCert, err := exists(certPath)
	if err != nil {
		return false, err
	}
	hasKey, err := exists(keyPath)
	if err != nil {
		return false, err
	}
	switch {
	case hasCert && !hasKey:
		return false, fmt.Errorf("%s without %s in %s", filepath.Base(certPath), filepath.Base(keyPath), filepath.Dir(certPath))
	case hasKey && !hasCert:
		return false, fmt.Errorf("%s without %s in %s", filepath.Base(keyPath), filepath.Base(certPath), filepath.Dir(certPath))
	}
	return hasCert, nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// MatchesIdentity says whether a certificate's URI SAN belongs to the principal: it is the
// principal's identity, or the identity followed by "/" and a certID (broker.WithCertID).
func MatchesIdentity(uri, identity string) bool {
	return uri == identity || strings.HasPrefix(uri, identity+"/") && len(uri) > len(identity)+1
}

// CertID is the part of a URI SAN after the principal's identity: the ID of the certificate, empty
// for one made before certIDs.
func CertID(uri, identity string) string {
	return strings.TrimPrefix(strings.TrimPrefix(uri, identity), "/")
}

// ClientCertIdentity is the one URI SAN of the certificate in the PEM file.
func ClientCertIdentity(certPath string) (string, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("%s holds no PEM certificate", certPath)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s: %w", certPath, err)
	}
	if len(leaf.URIs) != 1 {
		return "", fmt.Errorf("%s has %d URI names, want one", certPath, len(leaf.URIs))
	}
	return leaf.URIs[0].String(), nil
}

// ValidateClientCert is LoadClientCert without the pair.
func ValidateClientCert(certPath, keyPath, identity string) error {
	_, err := LoadClientCert(certPath, keyPath, identity)
	return err
}

// LoadClientCert reads the pair and checks that it loads, that the certificate is for the principal
// with the given identity (its one URI SAN, see MatchesIdentity) and that it is valid now. A client
// must not log in with a pair that cannot log in as that principal: it would fail at the server
// with a message that names nothing.
func LoadClientCert(certPath, keyPath, identity string) (tls.Certificate, error) {
	fail := func(reason string) (tls.Certificate, error) {
		return tls.Certificate{}, fmt.Errorf("the client certificate %s and %s %s; issue it again with `agent-busctl --direct cert issue`, "+
			"or delete both files to log in with the password if the server still allows it", certPath, keyPath, reason)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fail("do not form a pair (" + err.Error() + ")")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fail("cannot be read (" + err.Error() + ")")
	}
	if len(leaf.URIs) != 1 || !MatchesIdentity(leaf.URIs[0].String(), identity) {
		return fail("is not for " + identity)
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fail("is not valid now (" + leaf.NotBefore.Format(time.DateOnly) + " to " + leaf.NotAfter.Format(time.DateOnly) + ")")
	}
	return pair, nil
}

func stagedClientCertPaths(dir string) (certPath, keyPath string) {
	certPath, keyPath = ClientCertPaths(dir)
	return certPath + stagedSuffix, keyPath + stagedSuffix
}

// StageClientCert stores a certificate and its key under temporary names, which no client uses,
// until ActivateStagedClientCert makes them the login. It replaces earlier staged files.
func StageClientCert(dir string, certPEM, keyPEM []byte) error {
	certPath, keyPath := stagedClientCertPaths(dir)
	if err := files.Write(keyPath, keyPEM, files.SecretMode, true); err != nil {
		return err
	}
	return files.Write(certPath, certPEM, files.PublicMode, true)
}

// HasStagedClientCert says whether a staged pair waits in dir.
func HasStagedClientCert(dir string) (bool, error) {
	return hasPair(stagedClientCertPaths(dir))
}

// StagedClientCertIdentity is ClientCertIdentity of the staged certificate.
func StagedClientCertIdentity(dir string) (string, error) {
	stagedCert, _ := stagedClientCertPaths(dir)
	return ClientCertIdentity(stagedCert)
}

// ValidateStagedClientCert is ValidateClientCert for the staged pair.
func ValidateStagedClientCert(dir, identity string) error {
	stagedCert, stagedKey := stagedClientCertPaths(dir)
	return ValidateClientCert(stagedCert, stagedKey, identity)
}

// ActivateStagedClientCert makes the staged pair the certificate login, but only if it is valid
// for identity: a broken pair is never promoted over a working one.
func ActivateStagedClientCert(dir, identity string) error {
	if err := ValidateStagedClientCert(dir, identity); err != nil {
		return err
	}
	return activateStaged(dir)
}

// activateStaged renames the staged pair over the certificate login, key first.
func activateStaged(dir string) error {
	stagedCert, stagedKey := stagedClientCertPaths(dir)
	certPath, keyPath := ClientCertPaths(dir)
	if err := os.Rename(stagedKey, keyPath); err != nil {
		return err
	}
	return os.Rename(stagedCert, certPath)
}

// WriteClientCert stores a certificate and its key, replacing earlier ones (renewal). Both files
// are written under temporary names first, so a failure leaves the old pair intact.
func WriteClientCert(dir string, certPEM, keyPEM []byte) error {
	if err := StageClientCert(dir, certPEM, keyPEM); err != nil {
		return err
	}
	return activateStaged(dir)
}

// RemoveClientCert deletes the certificate login of dir, staged files included, so clients use
// the password again.
func RemoveClientCert(dir string) error {
	certPath, keyPath := ClientCertPaths(dir)
	stagedCert, stagedKey := stagedClientCertPaths(dir)
	for _, path := range []string{certPath, keyPath, stagedCert, stagedKey} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RetireClientCert renames the certificate login of dir out of the way (to *.retired) so clients
// use the password again, but the files stay for the owner to inspect or delete.
func RetireClientCert(dir string) error {
	certPath, keyPath := ClientCertPaths(dir)
	for _, path := range []string{certPath, keyPath} {
		if err := os.Rename(path, path+".retired"); err != nil && !errors.Is(err, os.ErrNotExist) {
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

// MonitorPrefsPath is the web monitor's shared preferences file, next to the password hash. While
// the hash is still in the old folder the preferences go there too, so one folder holds both.
func MonitorPrefsPath(configDir string) string {
	folder := filepath.Dir(MonitorPasswordPath(configDir))
	if _, err := os.Stat(MonitorPasswordPath(configDir)); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(configDir, oldMonitorPasswordFile)); err == nil {
			folder = filepath.Join(configDir, filepath.Dir(oldMonitorPasswordFile))
		}
	}
	return filepath.Join(folder, monitorPrefsFile)
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
