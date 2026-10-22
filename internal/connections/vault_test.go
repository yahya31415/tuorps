package connections_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

func testKeyring(t *testing.T, active string) *keyring.Keyring {
	t.Helper()
	kr, err := keyring.New(map[string][]byte{"k0": bytes.Repeat([]byte{1}, 32), "k1": bytes.Repeat([]byte{2}, 32)}, active)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func TestVault(t *testing.T) {
	v := connections.NewVault(testKeyring(t, "k1"), rand.Reader)
	row, other := id("000000005a01"), id("000000005a02")

	enc, err := v.Seal(row, core.FieldAccessToken, "EAA-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "EAA-secret-token") || !strings.HasPrefix(enc, "v1.k1.") {
		t.Fatalf("ciphertext %q is not a k1 keyring encoding of the token", enc)
	}

	t.Run("opens with the same row and field", func(t *testing.T) {
		if got, err := v.Open(row, core.FieldAccessToken, enc); err != nil || got != "EAA-secret-token" {
			t.Fatalf("Open = %q, %v", got, err)
		}
	})

	t.Run("refuses another row or field (AAD binding)", func(t *testing.T) {
		if _, err := v.Open(other, core.FieldAccessToken, enc); !errors.Is(err, keyring.ErrDecrypt) {
			t.Errorf("other row: %v", err)
		}
		if _, err := v.Open(row, core.FieldRefreshToken, enc); !errors.Is(err, keyring.ErrDecrypt) {
			t.Errorf("other field: %v", err)
		}
	})

	t.Run("an absent token is reported, not decrypted", func(t *testing.T) {
		if _, err := v.Open(row, core.FieldAccessToken, ""); !errors.Is(err, connections.ErrNoToken) {
			t.Errorf("Open(\"\") = %v, want ErrNoToken", err)
		}
		if _, err := v.Seal(row, core.FieldAccessToken, ""); !errors.Is(err, connections.ErrNoToken) {
			t.Errorf("Seal(\"\") = %v, want ErrNoToken", err)
		}
	})

	t.Run("reseal moves an old-key token to the active key, same plaintext", func(t *testing.T) {
		old, err := connections.NewVault(testKeyring(t, "k0"), rand.Reader).Seal(row, core.FieldPageToken, "page-token")
		if err != nil {
			t.Fatal(err)
		}
		resealed, changed, err := v.Reseal(row, core.FieldPageToken, old)
		if err != nil || !changed || !strings.HasPrefix(resealed, "v1.k1.") {
			t.Fatalf("Reseal = %q, %v, %v", resealed, changed, err)
		}
		if got, err := v.Open(row, core.FieldPageToken, resealed); err != nil || got != "page-token" {
			t.Errorf("after reseal Open = %q, %v", got, err)
		}
		same, changed, err := v.Reseal(row, core.FieldPageToken, resealed)
		if err != nil || changed || same != resealed {
			t.Errorf("resealing an active-key token = %q, %v, %v; want unchanged", same, changed, err)
		}
	})
}
