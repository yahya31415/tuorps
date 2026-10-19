package keyring_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func mustNew(t *testing.T, keys map[string][]byte, active string) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New(keys, active)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1)}, "k1")
	s, err := k.Seal(rand.Reader, []byte("totp-secret"), []byte("users/42"))
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != "k1" {
		t.Errorf("KeyID = %q", s.KeyID)
	}
	if bytes.Contains(s.Blob, []byte("totp-secret")) {
		t.Error("ciphertext contains the plaintext")
	}
	got, err := k.Open(s, []byte("users/42"))
	if err != nil || string(got) != "totp-secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealUsesAFreshNonceEachTime(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1)}, "k1")
	a, _ := k.Seal(rand.Reader, []byte("same"), nil)
	b, _ := k.Seal(rand.Reader, []byte("same"), nil)
	if bytes.Equal(a.Blob, b.Blob) {
		t.Fatal("two seals of the same plaintext are identical")
	}
}

func TestSealFailsWhenRandomnessFails(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1)}, "k1")
	if _, err := k.Seal(bytes.NewReader(nil), []byte("x"), nil); err == nil {
		t.Fatal("Seal succeeded without a nonce")
	}
}

func TestOpenRejects(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1), "k2": key(2)}, "k1")
	s, err := k.Seal(rand.Reader, []byte("payload"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		s    keyring.Sealed
		aad  string
		want error
	}{
		{"wrong aad", s, "other", keyring.ErrDecrypt},
		{"missing aad", s, "", keyring.ErrDecrypt},
		{"unknown key", keyring.Sealed{KeyID: "k9", Blob: s.Blob}, "aad", keyring.ErrUnknownKey},
		{"wrong key", keyring.Sealed{KeyID: "k2", Blob: s.Blob}, "aad", keyring.ErrDecrypt},
		{"truncated", keyring.Sealed{KeyID: "k1", Blob: s.Blob[:5]}, "aad", keyring.ErrDecrypt},
		{"empty", keyring.Sealed{KeyID: "k1"}, "aad", keyring.ErrDecrypt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := k.Open(tc.s, []byte(tc.aad)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRotationKeepsOldRecordsReadable(t *testing.T) {
	old := mustNew(t, map[string][]byte{"2026-01": key(1)}, "2026-01")
	s, err := old.Seal(rand.Reader, []byte("token"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	rotated := mustNew(t, map[string][]byte{"2026-01": key(1), "2026-10": key(2)}, "2026-10")
	if got, err := rotated.Open(s, []byte("aad")); err != nil || string(got) != "token" {
		t.Fatalf("old record after rotation: %q, %v", got, err)
	}
	fresh, _ := rotated.Seal(rand.Reader, []byte("token"), []byte("aad"))
	if fresh.KeyID != "2026-10" {
		t.Errorf("new records use %q, want the active key", fresh.KeyID)
	}
	if rotated.ActiveKeyID() != "2026-10" {
		t.Errorf("ActiveKeyID = %q", rotated.ActiveKeyID())
	}
}

func TestNewValidates(t *testing.T) {
	cases := []struct {
		name   string
		keys   map[string][]byte
		active string
	}{
		{"no keys", map[string][]byte{}, "k1"},
		{"active missing", map[string][]byte{"k1": key(1)}, "k2"},
		{"short key", map[string][]byte{"k1": key(1)[:16]}, "k1"},
		{"long key", map[string][]byte{"k1": append(key(1), 0)}, "k1"},
		{"bad id", map[string][]byte{"k 1": key(1)}, "k 1"},
		{"empty id", map[string][]byte{"": key(1)}, ""},
		{"overlong id", map[string][]byte{strings.Repeat("a", 33): key(1)}, strings.Repeat("a", 33)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := keyring.New(tc.keys, tc.active); !errors.Is(err, keyring.ErrInvalidKeyring) {
				t.Fatalf("err = %v, want ErrInvalidKeyring", err)
			}
		})
	}
}

func TestNewCopiesItsInput(t *testing.T) {
	keys := map[string][]byte{"k1": key(1)}
	k := mustNew(t, keys, "k1")
	s, _ := k.Seal(rand.Reader, []byte("x"), nil)
	keys["k1"][0] ^= 0xff // mutate the caller's slice afterwards
	if _, err := k.Open(s, nil); err != nil {
		t.Fatalf("keyring affected by caller mutation: %v", err)
	}
}

func TestParse(t *testing.T) {
	b64 := func(b byte) string { return base64.StdEncoding.EncodeToString(key(b)) }
	text := "# rotated quarterly\n\n2026-01:" + b64(1) + "\n 2026-10 : " + b64(2) + " \n"
	keys, err := keyring.Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(keys) != 2 || !bytes.Equal(keys["2026-01"], key(1)) || !bytes.Equal(keys["2026-10"], key(2)) {
		t.Fatalf("keys = %v", keys)
	}
	for name, bad := range map[string]string{
		"no separator":   "k1" + b64(1),
		"bad base64":     "k1:!!!",
		"duplicate id":   "k1:" + b64(1) + "\nk1:" + b64(2),
		"empty document": "# nothing\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := keyring.Parse(bad)
			if !errors.Is(err, keyring.ErrInvalidKeyring) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), b64(1)) {
				t.Errorf("error echoes key material: %v", err)
			}
		})
	}
}

func TestEncodeDecode(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1)}, "k1")
	s, _ := k.Seal(rand.Reader, []byte("payload"), nil)
	enc := s.Encode()
	if !strings.HasPrefix(enc, "v1.k1.") || strings.ContainsAny(enc, "+/= \n") {
		t.Fatalf("Encode = %q, want url-safe v1.<kid>.<data>", enc)
	}
	dec, err := keyring.Decode(enc)
	if err != nil || dec.KeyID != s.KeyID || !bytes.Equal(dec.Blob, s.Blob) {
		t.Fatalf("Decode = %+v, %v", dec, err)
	}
	for _, bad := range []string{"", "v1", "v1.k1", "v2.k1.AAAA", "v1..AAAA", "v1.k1.!!", "v1.k 1.AAAA"} {
		if _, err := keyring.Decode(bad); !errors.Is(err, keyring.ErrInvalidEncoding) {
			t.Errorf("Decode(%q) err = %v", bad, err)
		}
	}
}

// Property: any plaintext and AAD round-trip, and flipping any single bit of
// the sealed blob is detected.
func TestPropertyRoundTripAndTamperDetection(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(7)}, "k1")
	prop := func(plain, aad []byte, pos uint16, bit uint8) bool {
		s, err := k.Seal(rand.Reader, plain, aad)
		if err != nil {
			return false
		}
		got, err := k.Open(s, aad)
		if err != nil || !bytes.Equal(got, plain) {
			return false
		}
		tampered := keyring.Sealed{KeyID: s.KeyID, Blob: bytes.Clone(s.Blob)}
		tampered.Blob[int(pos)%len(tampered.Blob)] ^= 1 << (bit % 8)
		_, err = k.Open(tampered, aad)
		return errors.Is(err, keyring.ErrDecrypt)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func TestDeriveKey(t *testing.T) {
	k := mustNew(t, map[string][]byte{"k1": key(1), "k2": key(2)}, "k1")
	a := k.DeriveKey("audit-ip")
	if len(a) != 32 {
		t.Fatalf("len = %d", len(a))
	}
	if !bytes.Equal(a, k.DeriveKey("audit-ip")) {
		t.Error("derivation is not deterministic")
	}
	if bytes.Equal(a, k.DeriveKey("ratelimit")) {
		t.Error("different purposes share a key")
	}
	if bytes.Equal(a, key(1)) {
		t.Error("derived key equals the master key")
	}
	other := mustNew(t, map[string][]byte{"k1": key(1), "k2": key(2)}, "k2")
	if bytes.Equal(a, other.DeriveKey("audit-ip")) {
		t.Error("derivation ignores the active key")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add("v1.k1.AAAA")
	f.Add("v1.2026-10.c29tZQ")
	f.Add("v1..")
	f.Fuzz(func(t *testing.T, s string) {
		dec, err := keyring.Decode(s)
		if err != nil {
			return
		}
		if again, err := keyring.Decode(dec.Encode()); err != nil || again.KeyID != dec.KeyID || !bytes.Equal(again.Blob, dec.Blob) {
			t.Fatalf("Decode(Encode(x)) != x for %q", s)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add("k1:" + base64.StdEncoding.EncodeToString(key(1)))
	f.Add("# c\n\nk:YQ==")
	f.Fuzz(func(t *testing.T, s string) {
		keys, err := keyring.Parse(s)
		if err != nil {
			return
		}
		for id, k := range keys {
			if len(k) != 32 || id == "" {
				t.Fatalf("Parse accepted id %q with %d-byte key", id, len(k))
			}
		}
	})
}
