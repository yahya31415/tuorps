// Package keyring encrypts secrets at rest with AES-256-GCM (CLAUDE.md §3).
// Every record carries the ID of the key that sealed it, so keys can be
// rotated: the active key seals new records and all configured keys open old
// ones. Callers bind each record to its context with additional authenticated
// data (AAD), e.g. "users/<id>/totp", so ciphertexts cannot be swapped
// between rows.
package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	keySize     = 32
	maxKeyIDLen = 32
	encodingV1  = "v1"
)

var (
	ErrInvalidKeyring  = errors.New("keyring: invalid key configuration")
	ErrUnknownKey      = errors.New("keyring: record sealed with an unknown key")
	ErrDecrypt         = errors.New("keyring: cannot decrypt record")
	ErrInvalidEncoding = errors.New("keyring: invalid sealed encoding")
)

// Sealed is an encrypted record: the sealing key's ID and nonce||ciphertext.
type Sealed struct {
	KeyID string
	Blob  []byte
}

// Keyring holds the configured keys. It is immutable and safe for
// concurrent use.
type Keyring struct {
	active    string
	activeKey []byte
	aeads     map[string]cipher.AEAD
}

// New builds a keyring from 32-byte keys; active must be one of them.
func New(keys map[string][]byte, active string) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no keys", ErrInvalidKeyring)
	}
	aeads := make(map[string]cipher.AEAD, len(keys))
	for id, k := range keys {
		if !validKeyID(id) {
			return nil, fmt.Errorf("%w: key IDs must be 1-%d characters of [A-Za-z0-9_-]", ErrInvalidKeyring, maxKeyIDLen)
		}
		if len(k) != keySize {
			return nil, fmt.Errorf("%w: key %s is not %d bytes", ErrInvalidKeyring, id, keySize)
		}
		block, err := aes.NewCipher(append([]byte(nil), k...))
		if err != nil {
			return nil, fmt.Errorf("%w: key %s", ErrInvalidKeyring, id)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: key %s", ErrInvalidKeyring, id)
		}
		aeads[id] = aead
	}
	if _, ok := aeads[active]; !ok {
		return nil, fmt.Errorf("%w: active key %q is not configured", ErrInvalidKeyring, active)
	}
	return &Keyring{active: active, activeKey: append([]byte(nil), keys[active]...), aeads: aeads}, nil
}

// ActiveKeyID is the key that seals new records.
func (k *Keyring) ActiveKeyID() string { return k.active }

// DeriveKey returns a 32-byte subkey of the active key for one purpose
// (HKDF-SHA256), e.g. keying the audit log's client-IP hashes. Subkeys change
// when the active key rotates.
func (k *Keyring) DeriveKey(purpose string) []byte {
	// hkdf.Key fails only for lengths above 255*32 bytes; keySize is fixed at 32.
	sub, _ := hkdf.Key(sha256.New, k.activeKey, nil, "tuorps/"+purpose, keySize)
	return sub
}

// Seal encrypts plaintext with the active key and a fresh nonce from rand.
func (k *Keyring) Seal(rand io.Reader, plaintext, aad []byte) (Sealed, error) {
	aead := k.aeads[k.active]
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := io.ReadFull(rand, nonce); err != nil {
		return Sealed{}, fmt.Errorf("keyring: nonce: %w", err)
	}
	return Sealed{KeyID: k.active, Blob: aead.Seal(nonce, nonce, plaintext, aad)}, nil
}

// Open decrypts s, authenticating it against aad.
func (k *Keyring) Open(s Sealed, aad []byte) ([]byte, error) {
	aead, ok := k.aeads[s.KeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	if len(s.Blob) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, ct := s.Blob[:aead.NonceSize()], s.Blob[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// Encode renders s as a URL-safe string: "v1.<key-id>.<base64url blob>".
func (s Sealed) Encode() string {
	return encodingV1 + "." + s.KeyID + "." + base64.RawURLEncoding.EncodeToString(s.Blob)
}

// Decode parses the output of Encode.
func Decode(s string) (Sealed, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != encodingV1 || !validKeyID(parts[1]) {
		return Sealed{}, ErrInvalidEncoding
	}
	blob, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Sealed{}, ErrInvalidEncoding
	}
	return Sealed{KeyID: parts[1], Blob: blob}, nil
}

// Parse reads a key file: one "<key-id>:<base64 key>" per line, with blank
// lines and "#" comments ignored. Errors never include key material.
func Parse(text string) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, enc, ok := strings.Cut(line, ":")
		id, enc = strings.TrimSpace(id), strings.TrimSpace(enc)
		if !ok || !validKeyID(id) {
			return nil, fmt.Errorf("%w: line %d is not <key-id>:<base64 key>", ErrInvalidKeyring, n+1)
		}
		k, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(k) != keySize {
			return nil, fmt.Errorf("%w: line %d: key must be %d bytes, base64-encoded", ErrInvalidKeyring, n+1, keySize)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("%w: duplicate key ID on line %d", ErrInvalidKeyring, n+1)
		}
		keys[id] = k
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no keys", ErrInvalidKeyring)
	}
	return keys, nil
}

func validKeyID(id string) bool {
	if id == "" || len(id) > maxKeyIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
