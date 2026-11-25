// Package files writes the bus's local files with the permissions secrets need.
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// DirMode keeps config directories private to the owner.
	DirMode = 0o700
	// SecretMode is for private keys and passwords.
	SecretMode = 0o600
	// PublicMode is for certificates and public keys.
	PublicMode = 0o644

	lockPollInterval = 25 * time.Millisecond
)

// Write creates the file with exactly the given mode, and its parent directories with DirMode.
// It fails if the file exists, unless overwrite is set, so a secret is never replaced by accident.
func Write(path string, data []byte, mode os.FileMode, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return err
	}
	if overwrite {
		return replace(path, data, mode)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists", path)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	// OpenFile applies the umask and keeps the mode of an existing file.
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Close()
}

// replace writes a temporary file next to path and renames it over path, so a reader (the
// directory of agents is read while two fetches may write it) sees the old content or the new,
// never a truncated file.
func replace(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// Lock takes an exclusive lock on the file at path, creating it (and its directories) if needed. It
// waits at most wait for whoever holds it, which can be another process. The lock ends with
// unlock, or with the process.
func Lock(path string, wait time.Duration) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, SecretMode)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			file.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("another process holds %s (an approval in busd, or another agent-busctl); try again", path)
			}
			return nil, err
		}
		time.Sleep(lockPollInterval)
	}
}
