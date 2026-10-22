package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const (
	metaSignaturePrefix  = "sha256="
	linkedInSignedPrefix = "hmacsha256="
	maxHubChallengeLen   = 32
)

// VerifyMetaSignature checks an X-Hub-Signature-256 header ("sha256=" and a
// lowercase hex HMAC-SHA256 of the raw body) against each candidate secret,
// in constant time. Instagram Login passes two candidates because Meta does
// not document which app secret signs its webhooks (ADR 0010).
func VerifyMetaSignature(header string, body []byte, secrets ...[]byte) bool {
	hexSig, ok := strings.CutPrefix(header, metaSignaturePrefix)
	if !ok {
		return false
	}
	sig, ok := decodeHexSHA256(hexSig)
	if !ok {
		return false
	}
	valid := false
	for _, secret := range secrets {
		// Check every candidate so the time taken does not reveal which matched.
		if len(secret) > 0 && hmac.Equal(sig, mac(secret, body)) {
			valid = true
		}
	}
	return valid
}

// VerifyLinkedInSignature checks an X-LI-Signature header: a lowercase hex
// HMAC-SHA256, keyed with the client secret, of "hmacsha256=" followed by
// the raw body.
func VerifyLinkedInSignature(header string, body, secret []byte) bool {
	sig, ok := decodeHexSHA256(header)
	if !ok || len(secret) == 0 {
		return false
	}
	signed := make([]byte, 0, len(linkedInSignedPrefix)+len(body))
	signed = append(append(signed, linkedInSignedPrefix...), body...)
	return hmac.Equal(sig, mac(secret, signed))
}

// LinkedInChallengeResponse answers LinkedIn's endpoint validation: the
// lowercase hex HMAC-SHA256 of challengeCode keyed with the client secret.
func LinkedInChallengeResponse(challengeCode string, secret []byte) string {
	return hex.EncodeToString(mac(secret, []byte(challengeCode)))
}

// ValidHubChallenge reports whether a Meta hub.challenge may be echoed: 1 to
// 32 ASCII digits, so the endpoint never reflects arbitrary content.
func ValidHubChallenge(s string) bool {
	if len(s) == 0 || len(s) > maxHubChallengeLen {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// VerifyToken compares a received hub.verify_token with the configured one
// in constant time (lengths included). An empty value never matches.
func VerifyToken(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func mac(secret, msg []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return m.Sum(nil)
}

// decodeHexSHA256 decodes exactly 64 lowercase hex digits.
func decodeHexSHA256(s string) ([]byte, bool) {
	if len(s) != 2*sha256.Size {
		return nil, false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil, false
		}
	}
	b, err := hex.DecodeString(s)
	return b, err == nil
}
