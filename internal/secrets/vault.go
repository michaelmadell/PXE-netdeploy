package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ErrKeyMismatch: the value was sealed under a different key than the one
// in the data directory (restored onto other hardware without the key).
var ErrKeyMismatch = errors.New("secrets: sealed under a different key")

// Vault seals values into the store and opens them where they're used.
type Vault struct {
	st   *store.Store
	keys *Keys
}

// NewVault returns a vault over the store and the data directory's key.
func NewVault(st *store.Store, keys *Keys) *Vault { return &Vault{st: st, keys: keys} }

// Keys returns the key holder.
func (v *Vault) Keys() *Keys { return v.keys }

func sealer(r *Ring, kind string, plaintext []byte) store.SealFunc {
	return func(id string, vv int64) ([]byte, error) { return r.Seal(id, kind, vv, plaintext) }
}

// Create stores a new secret, creating the key on first use.
func (v *Vault) Create(ctx context.Context, name, description, kind string, plaintext []byte, hint *string) (store.Secret, error) {
	r, err := v.keys.Ring(true)
	if err != nil {
		return store.Secret{}, err
	}
	return v.st.CreateSecret(ctx, store.NewSecret{Name: name, Description: description, Kind: kind, Hint: hint,
		KeyFingerprint: r.Fingerprint(), Seal: sealer(r, kind, plaintext)})
}

// Replace stores a new value for a secret.
func (v *Vault) Replace(ctx context.Context, id string, plaintext []byte, hint *string, ifVersion int64) (store.Secret, error) {
	cur, err := v.st.Secret(ctx, id)
	if err != nil {
		return store.Secret{}, err
	}
	r, err := v.keys.Ring(true)
	if err != nil {
		return store.Secret{}, err
	}
	return v.st.ReplaceSecretValue(ctx, id, hint, r.Fingerprint(), sealer(r, cur.Kind, plaintext), ifVersion)
}

// Open decrypts a secret's value.
func (v *Vault) Open(ctx context.Context, id string) (store.Secret, []byte, error) {
	x, sealed, err := v.st.SecretValue(ctx, id)
	if err != nil {
		return store.Secret{}, nil, err
	}
	r, err := v.keys.Ring(false)
	if err != nil {
		return x, nil, err
	}
	if r.Fingerprint() != x.KeyFingerprint {
		return x, nil, fmt.Errorf("%w: secret %q (%s); import the key export from the original server or re-enter it", ErrKeyMismatch, x.Name, x.ID)
	}
	p, err := r.Open(x.ID, x.Kind, x.ValueVersion, sealed)
	return x, p, err
}

// Credential is a domain or share credential's value.
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Encode serialises a credential for sealing.
func (c Credential) Encode() []byte { b, _ := json.Marshal(c); return b }

// Credential opens a credential secret of the given kind.
func (v *Vault) Credential(ctx context.Context, id, kind string) (Credential, error) {
	x, p, err := v.Open(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if x.Kind != kind {
		return Credential{}, fmt.Errorf("secrets: %q is a %s, not a %s", x.Name, x.Kind, kind)
	}
	var c Credential
	if err := json.Unmarshal(p, &c); err != nil {
		return Credential{}, fmt.Errorf("secrets: %q: %w", x.Name, err)
	}
	return c, nil
}
