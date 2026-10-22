package core

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strings"
)

// ErrInvalidSignedRequest covers every malformed, unsigned or mis-signed
// signed_request; callers answer all of them the same way.
var ErrInvalidSignedRequest = errors.New("connections: invalid signed_request")

const maxSignedRequestLen = 8 << 10

// SignedRequest is the verified payload of a Meta or Instagram
// deauthorize or data-deletion callback.
type SignedRequest struct {
	UserID   string // app-scoped user ID
	IssuedAt int64  // Unix seconds
}

// ParseSignedRequest verifies "<base64url signature>.<base64url payload>":
// the signature is HMAC-SHA256 of the still-encoded payload, keyed with one
// of the candidate app secrets. The payload is decoded only after the
// signature matches, and must name the HMAC-SHA256 algorithm and a user.
func ParseSignedRequest(s string, secrets ...[]byte) (SignedRequest, error) {
	encSig, encPayload, ok := strings.Cut(s, ".")
	if !ok || len(s) > maxSignedRequestLen || encPayload == "" {
		return SignedRequest{}, ErrInvalidSignedRequest
	}
	sig, err := base64.RawURLEncoding.DecodeString(encSig)
	if err != nil {
		return SignedRequest{}, ErrInvalidSignedRequest
	}
	valid := false
	for _, secret := range secrets {
		if len(secret) > 0 && hmac.Equal(sig, mac(secret, []byte(encPayload))) {
			valid = true
		}
	}
	if !valid {
		return SignedRequest{}, ErrInvalidSignedRequest
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return SignedRequest{}, ErrInvalidSignedRequest
	}
	var p struct {
		Algorithm string `json:"algorithm"`
		IssuedAt  int64  `json:"issued_at"`
		UserID    string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Algorithm != "HMAC-SHA256" || p.UserID == "" {
		return SignedRequest{}, ErrInvalidSignedRequest
	}
	return SignedRequest{UserID: p.UserID, IssuedAt: p.IssuedAt}, nil
}
