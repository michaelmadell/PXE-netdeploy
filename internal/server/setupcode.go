package server

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
)

// SetupCodeFile holds the setup code while no account exists (ADR 0029).
const SetupCodeFile = "setup-code.txt"

// setupAlphabet has no 0, 1, O or I: 32 symbols, 5 bits each.
const setupAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// SetupCode is the one-time code that creates the first account.
type SetupCode struct {
	mu   sync.Mutex
	code string // without the dash; "" once used
	path string
}

// NewSetupCode makes a code and writes it to <dataDir>/setup-code.txt.
func NewSetupCode(dataDir string) (*SetupCode, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	for i := range b {
		b[i] = setupAlphabet[b[i]&31]
	}
	c := &SetupCode{code: string(b), path: filepath.Join(dataDir, SetupCodeFile)}
	text := "netdeploy setup code (valid until the first admin account is created):\n" + c.Display() + "\n"
	if err := secrets.WriteRestricted(c.path, []byte(text)); err != nil {
		return nil, err
	}
	return c, nil
}

// Display is the code as shown: XXXXX-XXXXX.
func (c *SetupCode) Display() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.code == "" {
		return ""
	}
	return c.code[:5] + "-" + c.code[5:]
}

// Path is the file the code was written to.
func (c *SetupCode) Path() string { return c.path }

// check compares in constant time, ignoring case, spaces and dashes.
func (c *SetupCode) check(s string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.code == "" {
		return false
	}
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	return subtle.ConstantTimeCompare([]byte(s), []byte(c.code)) == 1
}

// done forgets the code and deletes its file.
func (c *SetupCode) done() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.code = ""
	if err := os.Remove(c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveSetupCode deletes a leftover setup code file (an account exists).
func RemoveSetupCode(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, SetupCodeFile))
}
