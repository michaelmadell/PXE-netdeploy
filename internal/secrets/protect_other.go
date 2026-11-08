//go:build !windows

package secrets

import (
	"errors"
	"os"
)

// protect stores the raw key; the file is mode 0600, owned by the service
// user (ADR 0021).
func protect(key []byte) (byte, []byte, error) {
	return protectNone, append([]byte(nil), key...), nil
}

func unprotect([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI-protected keys can only be read on Windows")
}

func restrict(path string) error { return os.Chmod(path, 0o600) }
