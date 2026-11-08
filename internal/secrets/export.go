package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// MinPassphrase is the shortest export passphrase accepted.
const MinPassphrase = 12

const exportFormat = "netdeploy-secrets-key"

// Argon2id parameters for exports (as for the boot prompt password).
var exportKDF = kdfParams{Name: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

type kdfParams struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
	Salt      []byte `json:"salt"`
}

type exportFile struct {
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	KDF         kdfParams `json:"kdf"`
	Nonce       []byte    `json:"nonce"`
	Ciphertext  []byte    `json:"ciphertext"`
}

// ErrPassphrase: the passphrase doesn't open the export.
var ErrPassphrase = errors.New("secrets: wrong passphrase (or the file was changed)")

func exportAEAD(passphrase []byte, p kdfParams) (cipher.AEAD, error) {
	if p.Name != "argon2id" || p.Time == 0 || p.MemoryKiB == 0 || p.Threads == 0 || len(p.Salt) < 16 {
		return nil, fmt.Errorf("secrets: unsupported key derivation %q", p.Name)
	}
	k := argon2.IDKey(passphrase, p.Salt, p.Time, p.MemoryKiB, p.Threads, 32)
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Export returns the key encrypted with a passphrase, as JSON.
func (r *Ring) Export(passphrase []byte) ([]byte, error) {
	if len([]rune(string(passphrase))) < MinPassphrase {
		return nil, fmt.Errorf("secrets: the passphrase must be at least %d characters", MinPassphrase)
	}
	p := exportKDF
	p.Salt = make([]byte, 16)
	rand.Read(p.Salt)
	a, err := exportAEAD(passphrase, p)
	if err != nil {
		return nil, err
	}
	f := exportFile{Format: exportFormat, Version: 1, Fingerprint: r.Fingerprint(), KDF: p, Nonce: make([]byte, a.NonceSize())}
	rand.Read(f.Nonce)
	f.Ciphertext = a.Seal(nil, f.Nonce, r.key, []byte(exportFormat+f.Fingerprint))
	return json.MarshalIndent(f, "", "  ")
}

// Import opens an export made by Export.
func Import(b, passphrase []byte) (*Ring, error) {
	var f exportFile
	if err := json.Unmarshal(b, &f); err != nil || f.Format != exportFormat {
		return nil, errors.New("secrets: not a netdeploy key export")
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("secrets: key export version %d is newer than this server", f.Version)
	}
	a, err := exportAEAD(passphrase, f.KDF)
	if err != nil {
		return nil, err
	}
	if len(f.Nonce) != a.NonceSize() {
		return nil, ErrPassphrase
	}
	k, err := a.Open(nil, f.Nonce, f.Ciphertext, []byte(exportFormat+f.Fingerprint))
	if err != nil {
		return nil, ErrPassphrase
	}
	r, err := newRing(k)
	if err != nil {
		return nil, err
	}
	if r.Fingerprint() != f.Fingerprint {
		return nil, ErrPassphrase
	}
	return r, nil
}
