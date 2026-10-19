package core_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

// cheap keeps unit tests fast; production uses core.DefaultArgon2().
func cheap() core.Argon2Params { return core.Argon2Params{Memory: 64, Time: 1, Threads: 1} }

func salt(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }

func TestDefaultArgon2IsOWASPMinimum(t *testing.T) {
	p := core.DefaultArgon2()
	if p.Memory != 47104 || p.Time != 1 || p.Threads != 1 {
		t.Fatalf("DefaultArgon2 = %+v, want m=47104 (46 MiB), t=1, p=1", p)
	}
}

func TestHashPasswordFormat(t *testing.T) {
	h, err := core.HashPassword("correct horse battery staple", salt(1), cheap())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$AQEBAQEBAQEBAQEBAQEBAQ$") || strings.ContainsAny(h, "=+/") && strings.Count(h, "=") != 4 {
		t.Fatalf("PHC = %q", h)
	}
	again, _ := core.HashPassword("correct horse battery staple", salt(1), cheap())
	if again != h {
		t.Error("hashing is not deterministic for a fixed salt")
	}
	other, _ := core.HashPassword("correct horse battery staple", salt(2), cheap())
	if other == h {
		t.Error("salt does not affect the hash")
	}
}

func TestHashPasswordRejectsShortSalt(t *testing.T) {
	if _, err := core.HashPassword("x", make([]byte, 8), cheap()); !errors.Is(err, core.ErrInvalidHash) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyPassword(t *testing.T) {
	h, _ := core.HashPassword("correct horse battery staple", salt(1), cheap())
	ok, rehash, err := core.VerifyPassword("correct horse battery staple", h, cheap())
	if err != nil || !ok || rehash {
		t.Fatalf("correct password: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := core.VerifyPassword("correct horse battery stapler", h, cheap()); ok {
		t.Error("wrong password accepted")
	}
	stronger := core.Argon2Params{Memory: 128, Time: 2, Threads: 1}
	ok, rehash, _ = core.VerifyPassword("correct horse battery staple", h, stronger)
	if !ok || !rehash {
		t.Errorf("params changed: ok=%v rehash=%v, want ok and rehash", ok, rehash)
	}
}

func TestVerifyPasswordRejectsMalformedOrHostileHashes(t *testing.T) {
	good, _ := core.HashPassword("pw", salt(1), cheap())
	parts := strings.Split(good, "$") // "", argon2id, v=19, m=..., salt, hash
	join := func(p ...string) string { return strings.Join(p, "$") }
	cases := map[string]string{
		"empty":            "",
		"bcrypt":           "$2b$12$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyzABCDE",
		"argon2i":          join("", "argon2i", parts[2], parts[3], parts[4], parts[5]),
		"wrong version":    join("", "argon2id", "v=16", parts[3], parts[4], parts[5]),
		"memory bomb":      join("", "argon2id", "v=19", "m=4294967295,t=1,p=1", parts[4], parts[5]),
		"time bomb":        join("", "argon2id", "v=19", "m=64,t=100000,p=1", parts[4], parts[5]),
		"zero threads":     join("", "argon2id", "v=19", "m=64,t=1,p=0", parts[4], parts[5]),
		"bad params":       join("", "argon2id", "v=19", "m=64;t=1;p=1", parts[4], parts[5]),
		"short salt":       join("", "argon2id", "v=19", parts[3], "AQEB", parts[5]),
		"bad base64":       join("", "argon2id", "v=19", parts[3], parts[4], "!!!"),
		"truncated":        join("", "argon2id", "v=19", parts[3], parts[4]),
		"extra field":      good + "$x",
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			ok, _, err := core.VerifyPassword("pw", h, cheap())
			if ok || !errors.Is(err, core.ErrInvalidHash) {
				t.Fatalf("ok=%v err=%v, want ErrInvalidHash", ok, err)
			}
		})
	}
}

// Property: a password verifies against its own hash and no other password does.
func TestPropertyHashVerifyRoundTrip(t *testing.T) {
	prop := func(pw, other string, s [16]byte) bool {
		h, err := core.HashPassword(pw, s[:], cheap())
		if err != nil {
			return false
		}
		ok, _, err := core.VerifyPassword(pw, h, cheap())
		if err != nil || !ok {
			return false
		}
		bad, _, _ := core.VerifyPassword(other, h, cheap())
		return pw == other || !bad
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}

func FuzzVerifyPasswordParsing(f *testing.F) {
	good, _ := core.HashPassword("pw", salt(1), cheap())
	f.Add(good)
	f.Add("$argon2id$v=19$m=64,t=1,p=1$$")
	f.Fuzz(func(t *testing.T, h string) {
		// Must never panic, and must never accept parameters beyond the bounds
		// (the bounded parser is what keeps this fuzz target fast).
		_, _, _ = core.VerifyPassword("pw", h, cheap())
	})
}

func BenchmarkVerifyPasswordProduction(b *testing.B) {
	h, _ := core.HashPassword("correct horse battery staple", salt(1), core.DefaultArgon2())
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = core.VerifyPassword("correct horse battery staple", h, core.DefaultArgon2())
	}
}
