package settings

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: RFC 9106's second recommended option (64 MiB, t=3,
// p=4). The boot prompt checks one password per unlock attempt, and those are
// rate-limited, so the memory cost is affordable.
type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
	saltLen   int
	keyLen    uint32
}

var hashParams = argonParams{memoryKiB: 64 * 1024, time: 3, threads: 4, saltLen: 16, keyLen: 32}

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-format argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>.
func HashPassword(password string) (string, error) {
	p := hashParams
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("settings: password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memoryKiB, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errBadHash = errors.New("settings: malformed password hash")

// VerifyPassword reports whether password matches a hash from HashPassword,
// using the parameters recorded in the hash.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, errBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
