package whhttp_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/apitest"
)

const (
	metaSecret  = "meta-app-secret"
	metaVerify  = "meta-verify-token"
	igSecret    = "instagram-app-secret"
	igVerify    = "instagram-verify-token"
	liSecret    = "li-client-secret"
	metaPayload = `{"object":"page","entry":[{"id":"p1","time":1792000000,"changes":[{"field":"feed","value":{"item":"comment","message":"Spicy!"}}]}]}`
)

func credentials() map[string]string {
	return map[string]string{
		"META_APP_ID": "meta-app", "META_APP_SECRET": metaSecret, "META_LOGIN_CONFIG_ID": "cfg-1", "META_WEBHOOK_VERIFY_TOKEN": metaVerify,
		"INSTAGRAM_APP_ID": "ig-app", "INSTAGRAM_APP_SECRET": igSecret, "INSTAGRAM_WEBHOOK_VERIFY_TOKEN": igVerify,
		"LINKEDIN_CLIENT_ID": "li-app", "LINKEDIN_CLIENT_SECRET": liSecret,
	}
}

func hexMAC(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// signedRequest builds a Meta signed_request for userID.
func signedRequest(secret, userID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"algorithm":"HMAC-SHA256","issued_at":1792000000,"user_id":"` + userID + `"}`))
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)) + "." + payload
}

// raw sends a request with an exact body and headers, as a platform would:
// no cookies, no session.
func raw(t *testing.T, api *apitest.API, method, path, body string, header map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, api.Server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := api.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func jobs(t *testing.T, api *apitest.API) int {
	t.Helper()
	return api.Count(`SELECT count(*) FROM tuorps.river_job WHERE kind = 'webhooks.process'`)
}

func TestWebhooks(t *testing.T) {
	api := apitest.Start(t, apitest.WithEnv(credentials()))

	t.Run("meta: subscription handshake", func(t *testing.T) {
		q := func(mode, token, challenge string) string {
			return "/v1/webhooks/meta?" + url.Values{"hub.mode": {mode}, "hub.verify_token": {token}, "hub.challenge": {challenge}}.Encode()
		}
		if s, b := raw(t, api, http.MethodGet, q("subscribe", metaVerify, "1158201444"), "", nil); s != http.StatusOK || string(b) != "1158201444" {
			t.Errorf("valid handshake = %d %q", s, b)
		}
		for name, path := range map[string]string{
			"wrong token":        q("subscribe", "guess", "1158201444"),
			"instagram's token":  q("subscribe", igVerify, "1158201444"),
			"non-numeric echo":   q("subscribe", metaVerify, "<script>"),
			"not a subscription": q("unsubscribe", metaVerify, "1"),
		} {
			if s, b := raw(t, api, http.MethodGet, path, "", nil); s == http.StatusOK || strings.Contains(string(b), "<script>") {
				t.Errorf("%s: %d %q", name, s, b)
			}
		}
	})

	t.Run("meta: a signed event is persisted, enqueued and sealed", func(t *testing.T) {
		before := jobs(t, api)
		sig := "sha256=" + hexMAC(metaSecret, metaPayload)
		s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta", metaPayload, map[string]string{"Content-Type": "application/json", "X-Hub-Signature-256": sig})
		if s != http.StatusOK || jobs(t, api) != before+1 {
			t.Fatalf("status %d, jobs %d -> %d", s, before, jobs(t, api))
		}
		// Markers with '!' or '"' cannot occur in base64url ciphertext by chance.
		if n := api.Count(`SELECT count(*) FROM tuorps.river_job WHERE kind = 'webhooks.process' AND (args::text LIKE '%Spicy!%' OR args::text LIKE '%"object"%')`); n != 0 {
			t.Error("the payload is readable in the job table")
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.webhook_receipts WHERE provider = 'meta'`); n != 1 {
			t.Errorf("receipts = %d", n)
		}
		// Meta retries: the same delivery again is acknowledged but not re-enqueued.
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta", metaPayload, map[string]string{"X-Hub-Signature-256": sig}); s != http.StatusOK || jobs(t, api) != before+1 {
			t.Errorf("duplicate: status %d, jobs %d", s, jobs(t, api))
		}
		// Server-to-server: an Origin header does not trip cross-origin protection.
		other := strings.Replace(metaPayload, "Spicy", "Mild", 1)
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta", other, map[string]string{"X-Hub-Signature-256": "sha256=" + hexMAC(metaSecret, other), "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); s != http.StatusOK {
			t.Errorf("with a foreign Origin: %d", s)
		}
	})

	t.Run("meta: unsigned or mis-signed events are refused and not stored", func(t *testing.T) {
		before, receipts := jobs(t, api), api.Count(`SELECT count(*) FROM tuorps.webhook_receipts`)
		body := `{"object":"page","entry":[]}`
		for name, h := range map[string]map[string]string{
			"no signature":       nil,
			"wrong secret":       {"X-Hub-Signature-256": "sha256=" + hexMAC("guess", body)},
			"instagram's secret": {"X-Hub-Signature-256": "sha256=" + hexMAC(igSecret, body)},
			"signature of other": {"X-Hub-Signature-256": "sha256=" + hexMAC(metaSecret, body+" ")},
		} {
			if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta", body, h); s != http.StatusUnauthorized {
				t.Errorf("%s: %d", name, s)
			}
		}
		if jobs(t, api) != before || api.Count(`SELECT count(*) FROM tuorps.webhook_receipts`) != receipts {
			t.Error("a refused delivery was stored or enqueued")
		}
	})

	t.Run("instagram: either app secret signs (ADR 0010)", func(t *testing.T) {
		for _, secret := range []string{igSecret, metaSecret} {
			body := `{"object":"instagram","entry":[{"id":"17841400000000001","time":` + strings.Repeat("1", len(secret)) + `}]}`
			if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/instagram", body, map[string]string{"X-Hub-Signature-256": "sha256=" + hexMAC(secret, body)}); s != http.StatusOK {
				t.Errorf("signed with %s: %d", secret, s)
			}
		}
		body := `{"object":"instagram","entry":[]}`
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/instagram", body, map[string]string{"X-Hub-Signature-256": "sha256=" + hexMAC(liSecret, body)}); s != http.StatusUnauthorized {
			t.Errorf("signed with another secret: %d", s)
		}
		u := "/v1/webhooks/instagram?" + url.Values{"hub.mode": {"subscribe"}, "hub.verify_token": {igVerify}, "hub.challenge": {"42"}}.Encode()
		if s, b := raw(t, api, http.MethodGet, u, "", nil); s != http.StatusOK || string(b) != "42" {
			t.Errorf("instagram handshake = %d %q", s, b)
		}
	})

	t.Run("linkedin: challenge and signed notifications", func(t *testing.T) {
		const code = "6f1f0d4e-2b1a-4c7e-9a3b-1d2e3f4a5b6c"
		s, b := raw(t, api, http.MethodGet, "/v1/webhooks/linkedin?challengeCode="+code, "", nil)
		var resp struct {
			ChallengeCode     string `json:"challengeCode"`
			ChallengeResponse string `json:"challengeResponse"`
		}
		if s != http.StatusOK || json.Unmarshal(b, &resp) != nil || resp.ChallengeCode != code || resp.ChallengeResponse != hexMAC(liSecret, code) {
			t.Errorf("challenge = %d %s", s, b)
		}
		body := `{"type":"ORGANIZATION_SOCIAL_ACTION_NOTIFICATIONS","notifications":[{"notificationId":1,"action":"COMMENT"}]}`
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/linkedin", body, map[string]string{"X-LI-Signature": hexMAC(liSecret, "hmacsha256="+body)}); s != http.StatusOK {
			t.Errorf("signed notification: %d", s)
		}
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/linkedin", body, map[string]string{"X-LI-Signature": hexMAC(liSecret, body)}); s != http.StatusUnauthorized {
			t.Errorf("signature without the prefix: %d", s)
		}
	})

	t.Run("deauthorize callbacks are verified and enqueued", func(t *testing.T) {
		before := jobs(t, api)
		form := url.Values{"signed_request": {signedRequest(metaSecret, "10228374655566")}}.Encode()
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta/deauthorize", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); s != http.StatusOK || jobs(t, api) != before+1 {
			t.Errorf("meta deauthorize: %d, jobs +%d", s, jobs(t, api)-before)
		}
		bad := url.Values{"signed_request": {signedRequest("guess", "1")}}.Encode()
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/meta/deauthorize", bad, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); s != http.StatusBadRequest {
			t.Errorf("forged deauthorize: %d", s)
		}
		igForm := url.Values{"signed_request": {signedRequest(igSecret, "17841400000000009")}}.Encode()
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/instagram/deauthorize", igForm, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); s != http.StatusOK {
			t.Errorf("instagram deauthorize: %d", s)
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.river_job WHERE kind = 'webhooks.process' AND args::text LIKE '%10228374655566%'`); n != 0 {
			t.Error("the platform user id is readable in the job table")
		}
	})

	t.Run("data deletion answers with a confirmation code and a status page", func(t *testing.T) {
		form := url.Values{"signed_request": {signedRequest(metaSecret, "10228374655566")}}.Encode()
		s, b := raw(t, api, http.MethodPost, "/v1/webhooks/meta/data-deletion", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		var resp struct {
			URL              string `json:"url"`
			ConfirmationCode string `json:"confirmation_code"`
		}
		if s != http.StatusOK || json.Unmarshal(b, &resp) != nil || len(resp.ConfirmationCode) < 16 || resp.URL != "https://app.tuorps.test/data-deletion/"+resp.ConfirmationCode {
			t.Fatalf("data deletion = %d %s", s, b)
		}
		if n := api.Count(`SELECT count(*) FROM tuorps.data_deletion_requests WHERE confirmation_code = $1 AND status = 'pending' AND provider = 'meta'`, resp.ConfirmationCode); n != 1 {
			t.Errorf("request rows = %d", n)
		}
		s, b = raw(t, api, http.MethodGet, "/v1/data-deletion/"+resp.ConfirmationCode, "", nil)
		var status struct {
			ConfirmationCode string `json:"confirmation_code"`
			Status           string `json:"status"`
		}
		if s != http.StatusOK || json.Unmarshal(b, &status) != nil || status.Status != "pending" || bytes.Contains(b, []byte("10228374655566")) {
			t.Errorf("status page = %d %s", s, b)
		}
		if s, _ := raw(t, api, http.MethodGet, "/v1/data-deletion/not-a-code", "", nil); s != http.StatusNotFound {
			t.Errorf("unknown code: %d", s)
		}
		bad := url.Values{"signed_request": {"x.y"}}.Encode()
		if s, _ := raw(t, api, http.MethodPost, "/v1/webhooks/instagram/data-deletion", bad, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); s != http.StatusBadRequest {
			t.Errorf("forged data deletion: %d", s)
		}
	})
}

// A deployment without a platform's credentials does not expose its webhook.
func TestWebhooksOfUnconfiguredPlatforms(t *testing.T) {
	api := apitest.Start(t)
	for _, path := range []string{"/v1/webhooks/meta", "/v1/webhooks/instagram", "/v1/webhooks/linkedin", "/v1/webhooks/meta/data-deletion"} {
		if s, _ := raw(t, api, http.MethodPost, path, `{}`, nil); s != http.StatusNotFound {
			t.Errorf("POST %s: %d", path, s)
		}
	}
}

// CLAUDE.md: verify -> persist -> enqueue -> 200 in under 100 ms. Each
// iteration is a distinct delivery, so every one is persisted and enqueued.
func BenchmarkMetaWebhook(b *testing.B) {
	api := apitest.Start(b, apitest.WithEnv(credentials()))
	i := 0
	for b.Loop() {
		i++
		body := `{"object":"page","entry":[{"id":"p1","time":` + strconv.Itoa(i) + `}]}`
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, api.Server.URL+"/v1/webhooks/meta", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hexMAC(metaSecret, body))
		resp, err := api.Server.Client().Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			b.Fatalf("delivery %d: %v %v", i, err, resp)
		}
		_ = resp.Body.Close()
	}
}
