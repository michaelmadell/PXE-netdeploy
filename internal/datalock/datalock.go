// Package datalock stops two netdeployd processes (a server and an offline
// command such as restore or key import) from using one data directory at
// once. The lock is an OS file lock on <data>/netdeployd.lock, released
// automatically if the process dies.
package datalock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the lock file in the data directory.
const FileName = "netdeployd.lock"

// ErrLocked: another process holds the data directory.
var ErrLocked = errors.New("datalock: the data directory is in use by another netdeployd (stop the server first)")

// Lock is a held data directory lock.
type Lock struct{ f *os.File }

// Acquire takes the lock without waiting. ErrLocked if it's held.
// A data directory it creates is private to this account (ADR 0031): mode
// 700 on Linux, and on Windows an access list of SYSTEM, Administrators and
// this account, not inherited from the parent.
func Acquire(dataDir string) (*Lock, error) {
	_, statErr := os.Stat(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := makePrivate(dataDir); err != nil {
			return nil, fmt.Errorf("datalock: make %s private: %w", dataDir, err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dataDir, FileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("datalock: %w", err)
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlock(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
