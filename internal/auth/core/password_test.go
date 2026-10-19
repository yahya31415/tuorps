package core_test

import (
	"errors"
	"strings"
	"testing"
	"testing/quick"
	"unicode/utf8"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct{ in, display, normalized string }{
		{"priya@example.in", "priya@example.in", "priya@example.in"},
		{"  Priya.Sharma@Example.IN ", "Priya.Sharma@Example.IN", "priya.sharma@example.in"},
	}
	for _, tc := range cases {
		d, n, err := core.NormalizeEmail(tc.in)
		if err != nil || d != tc.display || n != tc.normalized {
			t.Errorf("NormalizeEmail(%q) = %q, %q, %v", tc.in, d, n, err)
		}
	}
	for _, bad := range []string{"", "not-an-email", "Priya <priya@example.in>", "a@b.in, c@d.in", "p@example.in\r\nBcc: x@y.in", strings.Repeat("a", 250) + "@x.in"} {
		if _, _, err := core.NormalizeEmail(bad); !errors.Is(err, core.ErrInvalidEmail) {
			t.Errorf("NormalizeEmail(%q) err = %v", bad, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	ok := []string{
		"correct horse battery staple",
		"Kerala-spices-2026!",
		"मेरा पासवर्ड बहुत लंबा है",      // non-Latin scripts are allowed and counted by character
		strings.Repeat("ab1", 42) + "xy", // 128 characters
	}
	for _, pw := range ok {
		if err := core.ValidatePassword(pw, "priya.sharma@example.in"); err != nil {
			t.Errorf("ValidatePassword(%q) = %v", pw, err)
		}
	}
	bad := []struct {
		pw   string
		want error
	}{
		{"short pass1", core.ErrPasswordTooShort},
		{"मेरापासवर्ड", core.ErrPasswordTooShort}, // 11 characters, more bytes
		{strings.Repeat("a1b2", 33), core.ErrPasswordTooLong},
		{"password1234", core.ErrPasswordCommon},
		{"Password1234", core.ErrPasswordCommon},
		{"qwertyuiopasdf", core.ErrPasswordCommon},
		{"aaaaaaaaaaaaaa", core.ErrPasswordCommon},
		{"123456789012345", core.ErrPasswordCommon},
		{"abcdefghijklmn", core.ErrPasswordCommon},
		{"priya.sharma2026", core.ErrPasswordPersonal},
		{"MyTuorpsAccount1", core.ErrPasswordPersonal},
		{"valid length\x00nul", core.ErrPasswordInvalid},
		{"bad utf-8 \xff\xfe ok?", core.ErrPasswordInvalid},
	}
	for _, tc := range bad {
		if err := core.ValidatePassword(tc.pw, "priya.sharma@example.in"); !errors.Is(err, tc.want) {
			t.Errorf("ValidatePassword(%q) = %v, want %v", tc.pw, err, tc.want)
		}
	}
}

func TestShortEmailLocalPartsDoNotBlockEverything(t *testing.T) {
	if err := core.ValidatePassword("a perfectly fine passphrase", "a@example.in"); err != nil {
		t.Fatalf("one-letter local part blocked a password: %v", err)
	}
}

// Property: whatever is accepted is 12-128 valid UTF-8 characters.
func TestPropertyAcceptedPasswordsAreWithinBounds(t *testing.T) {
	prop := func(pw string) bool {
		if core.ValidatePassword(pw, "x@example.in") != nil {
			return true
		}
		n := utf8.RuneCountInString(pw)
		return utf8.ValidString(pw) && n >= 12 && n <= 128
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func FuzzNormalizeEmail(f *testing.F) {
	f.Add("priya@example.in")
	f.Add("\"weird\"@x.in")
	f.Fuzz(func(t *testing.T, s string) {
		display, normalized, err := core.NormalizeEmail(s)
		if err != nil {
			return
		}
		if normalized != strings.ToLower(normalized) || strings.ContainsAny(display, "\r\n<>, ") || len(display) > 254 {
			t.Fatalf("NormalizeEmail(%q) = %q, %q", s, display, normalized)
		}
		if _, n2, err := core.NormalizeEmail(display); err != nil || n2 != normalized {
			t.Fatalf("not idempotent: %q -> %q -> %q (%v)", s, display, n2, err)
		}
	})
}
