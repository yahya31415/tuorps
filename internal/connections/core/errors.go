package core

import "encoding/json/v2"

// ErrorCode is the closed set of platform failure classes. It is the only
// thing kept from a platform error: stored as connections.last_error and
// usable as an error value (errors.Is) (ADR 0009).
type ErrorCode string

const (
	ErrReauthRequired      ErrorCode = "reauth_required"      // token revoked, expired or lost its role
	ErrRateLimited         ErrorCode = "rate_limited"         // HTTP 429
	ErrProviderUnavailable ErrorCode = "provider_unavailable" // 5xx or no response
	ErrRejected            ErrorCode = "rejected"             // any other refusal
)

func (c ErrorCode) Error() string { return "connections: provider error: " + string(c) }

// Retryable reports whether trying again later may succeed.
func (c ErrorCode) Retryable() bool { return c == ErrRateLimited || c == ErrProviderUnavailable }

// metaOAuthError is Graph API error 190: the access token is invalid,
// expired, revoked, or its user lost the Page role (subcodes 458-492).
const metaOAuthError = 190

// ClassifyProviderError maps a failed platform response (status 0 when no
// response arrived) to an ErrorCode. The body is inspected only for the
// documented auth-failure markers and is never retained.
func ClassifyProviderError(p Provider, status int, body []byte) ErrorCode {
	switch {
	case status == 0 || status >= 500:
		return ErrProviderUnavailable
	case status == 429:
		return ErrRateLimited
	case status == 401:
		return ErrReauthRequired
	}
	switch p {
	case ProviderMeta, ProviderInstagram:
		var r struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &r) == nil && r.Error.Code == metaOAuthError {
			return ErrReauthRequired
		}
	case ProviderGoogle, ProviderLinkedIn:
		var r struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) == nil && r.Error == "invalid_grant" {
			return ErrReauthRequired
		}
	}
	return ErrRejected
}
