package core_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Response shapes from docs/api-reference.md, "OAuth, tokens and webhook
// security"; token values are fakes.
func TestParseTokenResponse(t *testing.T) {
	cases := []struct {
		name    string
		p       core.Provider
		body    string
		access  string
		refresh string
		expires time.Duration
		user    string
		scopes  []string
	}{
		{"meta code exchange", core.ProviderMeta,
			`{"access_token":"EAAshort","token_type":"bearer","expires_in":5183944}`,
			"EAAshort", "", 5183944 * time.Second, "", nil},
		{"meta page token without expiry", core.ProviderMeta,
			`{"access_token":"EAApage","token_type":"bearer"}`,
			"EAApage", "", 0, "", nil},
		{"instagram wrapped short-lived", core.ProviderInstagram,
			`{"data":[{"access_token":"IGshort","user_id":"17841400000000001","permissions":"instagram_business_basic,instagram_business_manage_comments"}]}`,
			"IGshort", "", 0, "17841400000000001", []string{"instagram_business_basic", "instagram_business_manage_comments"}},
		{"instagram numeric user id", core.ProviderInstagram,
			`{"data":[{"access_token":"IGshort","user_id":17841400000000001,"permissions":"instagram_business_basic"}]}`,
			"IGshort", "", 0, "17841400000000001", []string{"instagram_business_basic"}},
		{"instagram flat long-lived", core.ProviderInstagram,
			`{"access_token":"IGlong","token_type":"bearer","expires_in":5184000}`,
			"IGlong", "", 5184000 * time.Second, "", nil},
		{"linkedin", core.ProviderLinkedIn,
			`{"access_token":"AQlinked","expires_in":5184000,"scope":"r_organization_social,w_organization_social"}`,
			"AQlinked", "", 5184000 * time.Second, "", []string{"r_organization_social", "w_organization_social"}},
		{"linkedin with refresh token", core.ProviderLinkedIn,
			`{"access_token":"AQlinked","expires_in":5184000,"refresh_token":"AQrefresh","refresh_token_expires_in":31536000,"scope":"r_organization_social"}`,
			"AQlinked", "AQrefresh", 5184000 * time.Second, "", []string{"r_organization_social"}},
		{"google with unknown fields", core.ProviderGoogle,
			`{"access_token":"ya29.a0","expires_in":3920,"token_type":"Bearer","scope":"https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/youtube.upload","refresh_token":"1//0refresh","refresh_token_expires_in":604799,"future_field":true}`,
			"ya29.a0", "1//0refresh", 3920 * time.Second, "", []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/youtube.upload"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, err := core.ParseTokenResponse(tc.p, []byte(tc.body))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if ts.AccessToken() != tc.access || ts.RefreshToken() != tc.refresh || ts.ExpiresIn() != tc.expires ||
				ts.ProviderUserID() != tc.user || !slices.Equal(ts.Scopes(), tc.scopes) {
				t.Errorf("got access=%q refresh=%q expires=%v user=%q scopes=%v",
					ts.AccessToken(), ts.RefreshToken(), ts.ExpiresIn(), ts.ProviderUserID(), ts.Scopes())
			}
		})
	}
}

func TestParseTokenResponseRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		p    core.Provider
		body string
	}{
		"not json":              {core.ProviderGoogle, `<html>`},
		"empty object":          {core.ProviderGoogle, `{}`},
		"empty token":           {core.ProviderLinkedIn, `{"access_token":"","expires_in":10}`},
		"negative expiry":       {core.ProviderGoogle, `{"access_token":"x","expires_in":-1}`},
		"absurd expiry":         {core.ProviderGoogle, `{"access_token":"x","expires_in":99999999999}`},
		"expiry that overflows": {core.ProviderGoogle, `{"access_token":"x","expires_in":18446744074}`}, // wraps to 0.29 s in int64 nanoseconds
		"duplicate keys":        {core.ProviderGoogle, `{"access_token":"x","access_token":"y"}`},
		"instagram empty data":  {core.ProviderInstagram, `{"data":[]}`},
		"instagram two entries": {core.ProviderInstagram, `{"data":[{"access_token":"a"},{"access_token":"b"}]}`},
		"unknown provider":      {"x", `{"access_token":"x"}`},
		"trailing data":         {core.ProviderMeta, `{"access_token":"x"} {}`},
	} {
		if _, err := core.ParseTokenResponse(tc.p, []byte(tc.body)); !errors.Is(err, core.ErrInvalidTokenResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidTokenResponse", name, err)
		}
	}
}

// Tokens never reach logs or formatted output by accident.
func TestTokenSetDoesNotLeak(t *testing.T) {
	ts, err := core.ParseTokenResponse(core.ProviderLinkedIn, []byte(`{"access_token":"SECRET-ACCESS","refresh_token":"SECRET-REFRESH","expires_in":60}`))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("tokens", "ts", ts)
	for _, out := range []string{fmt.Sprint(ts), fmt.Sprintf("%+v", ts), fmt.Sprintf("%#v", ts), buf.String()} {
		if strings.Contains(out, "SECRET") {
			t.Errorf("token leaked: %s", out)
		}
	}
}
