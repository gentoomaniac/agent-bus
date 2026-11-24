// Package files writes the bus's local files with the permissions secrets need.
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DirMode keeps config directories private to the owner.
	DirMode = 0o700
	// SecretMode is for private keys and passwords.
	SecretMode = 0o600
	// PublicMode is for certificates and public keys.
	PublicMode = 0o644
)

// Write creates the file with exactly the given mode, and its parent directories with DirMode.
// It fails if the file exists, unless overwrite is set, so a secret is never replaced by accident.
func Write(path string, data []byte, mode os.FileMode, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, mode)
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
