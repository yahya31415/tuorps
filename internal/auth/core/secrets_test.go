package core_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

func randomBytes(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*31)
	}
	return b
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := core.RecoveryCodes(randomBytes(core.RecoveryCodeEntropy, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("got %d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || strings.ToLower(c) != c || seen[c] {
			t.Errorf("code %q is malformed or repeated", c)
		}
		seen[c] = true
	}
	if _, err := core.RecoveryCodes(make([]byte, 10)); !errors.Is(err, core.ErrNotEnoughEntropy) {
		t.Errorf("short randomness: err = %v", err)
	}
}

func TestRecoveryCodeMatching(t *testing.T) {
	codes, _ := core.RecoveryCodes(randomBytes(core.RecoveryCodeEntropy, 9))
	var hashes [][32]byte
	for _, c := range codes {
		h, ok := core.HashRecoveryCode(c)
		if !ok {
			t.Fatalf("own code %q rejected", c)
		}
		hashes = append(hashes, h)
	}
	for _, input := range []string{codes[3], strings.ToUpper(codes[3]), strings.ReplaceAll(codes[3], "-", ""), " " + codes[3][:5] + " " + codes[3][6:] + " "} {
		if i, ok := core.MatchRecoveryCode(input, hashes); !ok || i != 3 {
			t.Errorf("MatchRecoveryCode(%q) = %d, %v; want 3", input, i, ok)
		}
	}
	for _, bad := range []string{"", "abcde-fghij0", "!!!!!-!!!!!", codes[3][:10]} {
		if _, ok := core.MatchRecoveryCode(bad, hashes); ok {
			t.Errorf("MatchRecoveryCode(%q) matched", bad)
		}
	}
	if _, ok := core.MatchRecoveryCode(codes[3], nil); ok {
		t.Error("matched with no remaining codes")
	}
}

func TestTokens(t *testing.T) {
	raw := randomBytes(32, 1)
	tok, err := core.EncodeToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token %q is not unpadded base64url of 32 bytes", tok)
	}
	h, ok := core.HashToken(tok)
	if !ok || h != sha256.Sum256(raw) {
		t.Fatalf("HashToken = %x, %v", h, ok)
	}
	if _, err := core.EncodeToken(raw[:16]); !errors.Is(err, core.ErrNotEnoughEntropy) {
		t.Errorf("16-byte token: err = %v", err)
	}
	for _, bad := range []string{"", "short", tok + "A", strings.Replace(tok, tok[:1], "+", 1), tok[:42] + "!"} {
		if _, ok := core.HashToken(bad); ok {
			t.Errorf("HashToken(%q) accepted", bad)
		}
	}
}

// Property: distinct randomness yields distinct tokens and hashes; tokens
// round-trip through HashToken.
func TestPropertyTokens(t *testing.T) {
	prop := func(a, b [32]byte) bool {
		ta, _ := core.EncodeToken(a[:])
		tb, _ := core.EncodeToken(b[:])
		ha, okA := core.HashToken(ta)
		hb, okB := core.HashToken(tb)
		return okA && okB && (bytes.Equal(a[:], b[:]) == (ta == tb)) && (ta == tb) == (ha == hb)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func FuzzMatchRecoveryCode(f *testing.F) {
	codes, _ := core.RecoveryCodes(randomBytes(core.RecoveryCodeEntropy, 3))
	var hashes [][32]byte
	for _, c := range codes {
		h, _ := core.HashRecoveryCode(c)
		hashes = append(hashes, h)
	}
	f.Add(codes[0])
	f.Add("zzzzz-zzzzz")
	f.Fuzz(func(t *testing.T, s string) {
		i, ok := core.MatchRecoveryCode(s, hashes)
		if ok && (i < 0 || i >= len(hashes)) {
			t.Fatalf("index %d out of range", i)
		}
	})
}
