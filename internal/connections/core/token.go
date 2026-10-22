package core

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// ErrInvalidTokenResponse covers any token endpoint reply that is not a
// well-formed success response.
var ErrInvalidTokenResponse = errors.New("connections: invalid token response")

// maxTokenLifetime bounds expires_in (the longest documented is 60 days;
// LinkedIn refresh tokens last 365).
const maxTokenLifetime = 400 * 24 * time.Hour

// TokenSet is a parsed token endpoint response. Its tokens are reachable
// only through accessors, and it formats and logs as redacted.
type TokenSet struct {
	access, refresh string
	expiresIn       time.Duration // 0: the response gave no lifetime
	scopes          []string
	providerUserID  string
}

func (t TokenSet) AccessToken() string      { return t.access }
func (t TokenSet) RefreshToken() string     { return t.refresh }
func (t TokenSet) ExpiresIn() time.Duration { return t.expiresIn }
func (t TokenSet) Scopes() []string         { return slices.Clone(t.scopes) }
func (t TokenSet) ProviderUserID() string   { return t.providerUserID }
func (t TokenSet) String() string           { return "TokenSet{[REDACTED]}" }
func (t TokenSet) GoString() string         { return t.String() }
func (t TokenSet) LogValue() slog.Value     { return slog.StringValue("[REDACTED]") }

type tokenJSON struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	ExpiresIn    int64          `json:"expires_in"`
	Scope        string         `json:"scope"`       // Google: spaces; LinkedIn: commas
	Permissions  string         `json:"permissions"` // Instagram: commas
	UserID       jsontext.Value `json:"user_id"`     // Instagram: string or number
	Data         []tokenJSON    `json:"data"`        // Instagram code exchange wraps its reply
}

// ParseTokenResponse parses a successful token endpoint body from p.
// Lifetimes come from the response, never from documentation (ADR 0009).
func ParseTokenResponse(p Provider, body []byte) (TokenSet, error) {
	if _, err := ParseProvider(string(p)); err != nil {
		return TokenSet{}, ErrInvalidTokenResponse
	}
	var r tokenJSON
	if err := json.Unmarshal(body, &r); err != nil {
		return TokenSet{}, ErrInvalidTokenResponse
	}
	if p == ProviderInstagram && r.Data != nil {
		if len(r.Data) != 1 {
			return TokenSet{}, ErrInvalidTokenResponse
		}
		r = r.Data[0]
	}
	// Bound the seconds before converting: a large expires_in would
	// overflow int64 nanoseconds and could wrap to a small positive value.
	if r.AccessToken == "" || r.ExpiresIn < 0 || r.ExpiresIn > int64(maxTokenLifetime/time.Second) {
		return TokenSet{}, ErrInvalidTokenResponse
	}
	lifetime := time.Duration(r.ExpiresIn) * time.Second
	user, ok := userID(r.UserID)
	if !ok {
		return TokenSet{}, ErrInvalidTokenResponse
	}
	return TokenSet{
		access:         r.AccessToken,
		refresh:        r.RefreshToken,
		expiresIn:      lifetime,
		scopes:         splitScopes(r.Scope + "," + r.Permissions),
		providerUserID: user,
	}, nil
}

// userID accepts a JSON string or an unsigned integer literal.
func userID(v jsontext.Value) (string, bool) {
	if len(v) == 0 {
		return "", true
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, true
	}
	digits := string(v)
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}
	return digits, true
}

func splitScopes(s string) []string {
	return slices.DeleteFunc(strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }), func(x string) bool { return x == "" })
}
