package core_test

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Platform errors map to a closed set of codes; nothing from the response
// body is kept (ADR 0009). Shapes per docs/api-reference.md.
func TestClassifyProviderError(t *testing.T) {
	cases := []struct {
		name   string
		p      core.Provider
		status int
		body   string
		want   core.ErrorCode
	}{
		{"meta expired token", core.ProviderMeta, 400, `{"error":{"message":"Error validating access token","type":"OAuthException","code":190,"error_subcode":463}}`, core.ErrReauthRequired},
		{"meta password changed", core.ProviderMeta, 400, `{"error":{"type":"OAuthException","code":190,"error_subcode":460}}`, core.ErrReauthRequired},
		{"meta lost page role", core.ProviderMeta, 400, `{"error":{"type":"OAuthException","code":190,"error_subcode":492}}`, core.ErrReauthRequired},
		{"meta other oauth error", core.ProviderMeta, 400, `{"error":{"type":"OAuthException","code":100}}`, core.ErrRejected},
		{"instagram expired", core.ProviderInstagram, 400, `{"error":{"message":"x","type":"OAuthException","code":190}}`, core.ErrReauthRequired},
		{"google invalid_grant", core.ProviderGoogle, 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, core.ErrReauthRequired},
		{"google bad request", core.ProviderGoogle, 400, `{"error":"invalid_request"}`, core.ErrRejected},
		{"linkedin invalid_grant", core.ProviderLinkedIn, 400, `{"error":"invalid_grant","error_description":"x"}`, core.ErrReauthRequired},
		{"any 401", core.ProviderLinkedIn, 401, `{"status":401,"message":"Invalid access token"}`, core.ErrReauthRequired},
		{"rate limited", core.ProviderGoogle, 429, ``, core.ErrRateLimited},
		{"server error", core.ProviderMeta, 503, `<html>`, core.ErrProviderUnavailable},
		{"no response", core.ProviderLinkedIn, 0, ``, core.ErrProviderUnavailable},
		{"other 4xx with junk", core.ProviderLinkedIn, 403, `not json`, core.ErrRejected},
		{"unexpected success status", core.ProviderGoogle, 200, `{}`, core.ErrRejected},
	}
	for _, tc := range cases {
		if got := core.ClassifyProviderError(tc.p, tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestErrorCodeRetryable(t *testing.T) {
	for code, want := range map[core.ErrorCode]bool{
		core.ErrRateLimited: true, core.ErrProviderUnavailable: true,
		core.ErrReauthRequired: false, core.ErrRejected: false,
	} {
		if code.Retryable() != want {
			t.Errorf("%s.Retryable() = %v", code, !want)
		}
	}
}
