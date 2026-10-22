package connections

import (
	"errors"
	"fmt"
	"io"
	"uuid"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

// ErrNoToken means a token column is empty: never stored, or wiped.
var ErrNoToken = errors.New("connections: no token")

// Vault seals and opens platform tokens with the keyring. Every ciphertext
// is bound to its row and field through the AAD (ADR 0009).
type Vault struct {
	keys *keyring.Keyring
	rand io.Reader
}

// NewVault returns a Vault sealing with the keyring's active key.
func NewVault(keys *keyring.Keyring, rand io.Reader) Vault {
	return Vault{keys: keys, rand: rand}
}

// Seal encrypts token for rowID's field and returns the stored encoding.
func (v Vault) Seal(rowID uuid.UUID, f core.TokenField, token string) (string, error) {
	if token == "" {
		return "", ErrNoToken
	}
	s, err := v.keys.Seal(v.rand, []byte(token), core.TokenAAD(rowID, f))
	if err != nil {
		return "", fmt.Errorf("connections: seal %s: %w", f, err)
	}
	return s.Encode(), nil
}

// Open decrypts a stored token of rowID's field.
func (v Vault) Open(rowID uuid.UUID, f core.TokenField, enc string) (string, error) {
	if enc == "" {
		return "", ErrNoToken
	}
	s, err := keyring.Decode(enc)
	if err != nil {
		return "", fmt.Errorf("connections: open %s: %w", f, err)
	}
	plain, err := v.keys.Open(s, core.TokenAAD(rowID, f))
	if err != nil {
		return "", fmt.Errorf("connections: open %s: %w", f, err)
	}
	return string(plain), nil
}

// Reseal re-encrypts a token sealed with an older key under the active one,
// reporting whether it changed. Tokens already on the active key are
// returned as they are.
func (v Vault) Reseal(rowID uuid.UUID, f core.TokenField, enc string) (string, bool, error) {
	s, err := keyring.Decode(enc)
	if err != nil {
		return "", false, fmt.Errorf("connections: reseal %s: %w", f, err)
	}
	if s.KeyID == v.keys.ActiveKeyID() {
		return enc, false, nil
	}
	plain, err := v.Open(rowID, f, enc)
	if err != nil {
		return "", false, err
	}
	out, err := v.Seal(rowID, f, plain)
	return out, err == nil, err
}
