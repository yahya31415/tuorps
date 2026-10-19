package core

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

const (
	recoveryCodeCount = 10
	recoveryCodeChars = 10 // 50 bits of base32
	recoveryAlphabet  = "abcdefghijklmnopqrstuvwxyz234567"
	tokenBytes        = 32

	// RecoveryCodeEntropy is the randomness RecoveryCodes needs.
	RecoveryCodeEntropy = recoveryCodeCount * 8
)

// ErrNotEnoughEntropy reports too few random bytes.
var ErrNotEnoughEntropy = errors.New("auth: not enough randomness")

// RecoveryCodes formats ten single-use codes ("abcde-fghij", 50 bits each)
// from RecoveryCodeEntropy random bytes.
func RecoveryCodes(random []byte) ([]string, error) {
	if len(random) < RecoveryCodeEntropy {
		return nil, ErrNotEnoughEntropy
	}
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		v := binary.BigEndian.Uint64(random[i*8 : i*8+8])
		var b [recoveryCodeChars]byte
		for j := range b {
			b[j] = recoveryAlphabet[v&31]
			v >>= 5
		}
		codes[i] = string(b[:5]) + "-" + string(b[5:])
	}
	return codes, nil
}

// HashRecoveryCode normalises a code (case, dashes and spaces ignored) and
// returns its SHA-256, the only form stored.
func HashRecoveryCode(code string) ([32]byte, bool) {
	n := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(code)))
	if len(n) != recoveryCodeChars || strings.ContainsFunc(n, func(r rune) bool { return !strings.ContainsRune(recoveryAlphabet, r) }) {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(n)), true
}

// MatchRecoveryCode compares input against every unused code hash in
// constant time and returns the index of the match.
func MatchRecoveryCode(input string, unused [][32]byte) (int, bool) {
	h, ok := HashRecoveryCode(input)
	if !ok {
		return 0, false
	}
	match := -1
	for i, u := range unused {
		if subtle.ConstantTimeCompare(h[:], u[:]) == 1 && match < 0 {
			match = i
		}
	}
	return match, match >= 0
}

// EncodeToken renders 32 random bytes as an unpadded base64url token
// (sessions, email links, invitations).
func EncodeToken(random []byte) (string, error) {
	if len(random) < tokenBytes {
		return "", ErrNotEnoughEntropy
	}
	return base64.RawURLEncoding.EncodeToString(random[:tokenBytes]), nil
}

// HashToken returns the SHA-256 of a token's bytes, the only form stored.
func HashToken(token string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}
