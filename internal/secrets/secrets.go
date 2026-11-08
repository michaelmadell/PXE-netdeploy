// Package secrets encrypts secret values under one per-installation data
// key (ADR 0021). The key lives in <data>/secrets.key, protected by DPAPI
// (machine scope) on Windows and by file permissions elsewhere.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileName is the key file in the data directory.
const FileName = "secrets.key"

var (
	// ErrNoKey: the key file doesn't exist (no secret was ever stored).
	ErrNoKey = errors.New("secrets: no key file")
	// ErrDecrypt: a value doesn't decrypt under this key (wrong key, or the
	// row was tampered with or moved).
	ErrDecrypt = errors.New("secrets: value does not decrypt with this key")
)

const (
	magic          = "NDSECKEY"
	fileVersion    = 1
	protectDPAPI   = 1 // payload is a DPAPI blob (machine scope)
	protectNone    = 2 // payload is the raw key; the file's permissions protect it
	keySize        = 32
	sealVersion    = 1
	nonceSize      = 12
	dpapiEntropyID = "netdeploy secrets key v1"
)

// Ring holds the data key.
type Ring struct {
	key  []byte
	aead cipher.AEAD
}

func newRing(key []byte) (*Ring, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: key is %d bytes, want %d", len(key), keySize)
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Ring{key: bytes.Clone(key), aead: a}, nil
}

// NewRing returns a ring with a fresh random key (not saved).
func NewRing() (*Ring, error) {
	k := make([]byte, keySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return newRing(k)
}

// Fingerprint identifies the key without revealing it: hex SHA-256.
func (r *Ring) Fingerprint() string {
	s := sha256.Sum256(r.key)
	return hex.EncodeToString(s[:])
}

// aad binds a ciphertext to its row: swapping values between secrets, kinds
// or versions makes them fail to decrypt.
func aad(id, kind string, valueVersion int64) []byte {
	b := []byte("netdeploy-secret\x00" + id + "\x00" + kind + "\x00")
	return binary.BigEndian.AppendUint64(b, uint64(valueVersion))
}

// Seal encrypts a value: version byte, nonce, ciphertext and tag.
func (r *Ring) Seal(id, kind string, valueVersion int64, plaintext []byte) ([]byte, error) {
	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+r.aead.Overhead())
	out[0] = sealVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return r.aead.Seal(out, out[1:1+nonceSize], plaintext, aad(id, kind, valueVersion)), nil
}

// Open decrypts what Seal produced for the same id, kind and version.
func (r *Ring) Open(id, kind string, valueVersion int64, sealed []byte) ([]byte, error) {
	if len(sealed) < 1+nonceSize+r.aead.Overhead() || sealed[0] != sealVersion {
		return nil, ErrDecrypt
	}
	p, err := r.aead.Open(nil, sealed[1:1+nonceSize], sealed[1+nonceSize:], aad(id, kind, valueVersion))
	if err != nil {
		return nil, ErrDecrypt
	}
	return p, nil
}

// Load reads the key file in dataDir. ErrNoKey if there is none.
func Load(dataDir string) (*Ring, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read key: %w", err)
	}
	if len(b) < len(magic)+2 || string(b[:len(magic)]) != magic || b[len(magic)] != fileVersion {
		return nil, fmt.Errorf("secrets: %s is not a netdeploy key file", FileName)
	}
	payload := b[len(magic)+2:]
	switch b[len(magic)+1] {
	case protectNone:
		return newRing(payload)
	case protectDPAPI:
		k, err := unprotect(payload)
		if err != nil {
			return nil, fmt.Errorf("secrets: unwrap key (DPAPI; a key from another machine or Windows installation can't be read here, import its export instead): %w", err)
		}
		return newRing(k)
	}
	return nil, fmt.Errorf("secrets: unknown key protection %d", b[len(magic)+1])
}

// Save writes the ring's key to dataDir, protected for this platform. It
// refuses to replace an existing key file unless replace is set.
func (r *Ring) Save(dataDir string, replace bool) error {
	p := filepath.Join(dataDir, FileName)
	if _, err := os.Stat(p); err == nil && !replace {
		return fmt.Errorf("secrets: %s already exists", p)
	}
	mode, payload, err := protect(r.key)
	if err != nil {
		return fmt.Errorf("secrets: protect key: %w", err)
	}
	b := append([]byte(magic), fileVersion, mode)
	b = append(b, payload...)
	tmp := p + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("secrets: write key: %w", err)
	}
	if err := restrict(tmp); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("secrets: restrict key file: %w", err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("secrets: write key: %w", err)
	}
	return nil
}

// Keys loads the key on first use and creates it when asked to.
type Keys struct {
	dir  string
	mu   sync.Mutex
	ring *Ring
}

// NewKeys returns the key holder for a data directory.
func NewKeys(dataDir string) *Keys { return &Keys{dir: dataDir} }

// Ring returns the key, creating and saving one if there is none and create
// is set; ErrNoKey otherwise.
func (k *Keys) Ring(create bool) (*Ring, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ring != nil {
		return k.ring, nil
	}
	r, err := Load(k.dir)
	if errors.Is(err, ErrNoKey) && create {
		if r, err = NewRing(); err == nil {
			err = r.Save(k.dir, false)
		}
	}
	if err != nil {
		return nil, err
	}
	k.ring = r
	return r, nil
}
