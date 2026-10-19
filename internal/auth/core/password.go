// Package core holds the pure authentication rules: email normalisation,
// password policy and hashing, TOTP, recovery codes, sessions and the login
// decision. No I/O, clock or randomness: callers pass them in.
package core

import (
	_ "embed"
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minPasswordLen = 12  // ASVS 2.1.1
	maxPasswordLen = 128 // ASVS 2.1.2 (at least 64 must be allowed)
	maxEmailLen    = 254
)

var (
	ErrInvalidEmail     = errors.New("auth: invalid email address")
	ErrPasswordTooShort = errors.New("auth: password must be at least 12 characters")
	ErrPasswordTooLong  = errors.New("auth: password must be at most 128 characters")
	ErrPasswordCommon   = errors.New("auth: password is too common or predictable")
	ErrPasswordPersonal = errors.New("auth: password must not contain your email or the product name")
	ErrPasswordInvalid  = errors.New("auth: password contains invalid characters")
)

// commonPasswords is the embedded list (read-only; //go:embed requires a
// package-level variable).
//
//go:embed common_passwords.txt
var commonPasswords string

// NormalizeEmail accepts a bare address (no display name, no list) and
// returns it trimmed for display and lowercased for uniqueness.
func NormalizeEmail(s string) (display, normalized string, err error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxEmailLen || strings.ContainsAny(s, "\r\n<>,; \t") {
		return "", "", ErrInvalidEmail
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", "", ErrInvalidEmail
	}
	return s, strings.ToLower(s), nil
}

// ValidatePassword applies the password policy: 12-128 characters of valid
// UTF-8 without control characters, no composition rules, and not common,
// predictable or built from the user's email.
func ValidatePassword(pw, email string) error {
	if !utf8.ValidString(pw) || strings.ContainsFunc(pw, unicode.IsControl) {
		return ErrPasswordInvalid
	}
	switch n := utf8.RuneCountInString(pw); {
	case n < minPasswordLen:
		return ErrPasswordTooShort
	case n > maxPasswordLen:
		return ErrPasswordTooLong
	}
	lower := strings.ToLower(pw)
	if isCommon(lower) || isSequence(lower) || isRepetition(lower) {
		return ErrPasswordCommon
	}
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	if (len(local) >= 3 && strings.Contains(lower, local)) || strings.Contains(lower, "tuorps") {
		return ErrPasswordPersonal
	}
	return nil
}

func isCommon(lower string) bool {
	for line := range strings.Lines(commonPasswords) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") && line == lower {
			return true
		}
	}
	return false
}

// isSequence reports whether s runs along the alphabet, a keyboard row or
// the digits, forwards or backwards.
func isSequence(s string) bool {
	for _, seq := range []string{
		"abcdefghijklmnopqrstuvwxyz",
		"qwertyuiopasdfghjklzxcvbnm",
		strings.Repeat("1234567890", maxPasswordLen/10+2),
	} {
		if strings.Contains(seq, s) || strings.Contains(reverse(seq), s) {
			return true
		}
	}
	return false
}

// isRepetition reports whether s is a unit of up to three characters repeated.
func isRepetition(s string) bool {
	r := []rune(s)
	for period := 1; period <= 3; period++ {
		repeated := true
		for i := period; i < len(r); i++ {
			if r[i] != r[i-period] {
				repeated = false
				break
			}
		}
		if repeated {
			return true
		}
	}
	return false
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
