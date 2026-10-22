package core_test

import (
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Known-answer vectors computed independently with `openssl dgst -sha256 -hmac`.
const (
	metaBody   = `{"object":"page","entry":[]}`
	metaSecret = "meta-app-secret"
	metaSig    = "5dcb51eb590634700c8d9cc1fde4d2d5981ca7bb83f250eb3fb71bfd849057ed"

	liBody      = `{"type":"ORGANIZATION_SOCIAL_ACTION_NOTIFICATIONS"}`
	liSecret    = "li-client-secret"
	liSig       = "53ce2f4e8a33b4232fc33185edca420016244f3bd8f11e31fde88b714da59f1b" // over "hmacsha256=" + body
	liChallenge = "6f1f0d4e-2b1a-4c7e-9a3b-1d2e3f4a5b6c"
	liResponse  = "8ffba6a625003e92be0b78a352ea2657a168034f2decaba1ceebe3251d2a5eb7"
)

func TestVerifyMetaSignature(t *testing.T) {
	body, secret := []byte(metaBody), []byte(metaSecret)
	if !core.VerifyMetaSignature("sha256="+metaSig, body, secret) {
		t.Fatal("valid signature rejected")
	}
	// Instagram Login: either of our two secrets may be the signer (ADR 0010).
	if !core.VerifyMetaSignature("sha256="+metaSig, body, []byte("instagram-app-secret"), secret) {
		t.Error("valid signature rejected when its secret is the second candidate")
	}
	rejected := map[string]struct {
		header  string
		body    string
		secrets [][]byte
	}{
		"tampered body":       {"sha256=" + metaSig, metaBody + " ", [][]byte{secret}},
		"wrong secret":        {"sha256=" + metaSig, metaBody, [][]byte{[]byte("other")}},
		"no secrets":          {"sha256=" + metaSig, metaBody, nil},
		"empty secret":        {"sha256=" + metaSig, metaBody, [][]byte{{}}},
		"missing header":      {"", metaBody, [][]byte{secret}},
		"sha1 header":         {"sha1=" + metaSig, metaBody, [][]byte{secret}},
		"no prefix":           {metaSig, metaBody, [][]byte{secret}},
		"uppercase hex":       {"sha256=" + strings.ToUpper(metaSig), metaBody, [][]byte{secret}},
		"truncated":           {"sha256=" + metaSig[:62], metaBody, [][]byte{secret}},
		"not hex":             {"sha256=" + strings.Repeat("zz", 32), metaBody, [][]byte{secret}},
		"trailing whitespace": {"sha256=" + metaSig + " ", metaBody, [][]byte{secret}},
	}
	for name, tc := range rejected {
		if core.VerifyMetaSignature(tc.header, []byte(tc.body), tc.secrets...) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyLinkedInSignature(t *testing.T) {
	body, secret := []byte(liBody), []byte(liSecret)
	if !core.VerifyLinkedInSignature(liSig, body, secret) {
		t.Fatal("valid signature rejected")
	}
	for name, tc := range map[string]struct{ header, body, secret string }{
		"tampered body":    {liSig, liBody + " ", liSecret},
		"wrong secret":     {liSig, liBody, "other"},
		"empty secret":     {liSig, liBody, ""},
		"missing header":   {"", liBody, liSecret},
		"prefix in header": {"hmacsha256=" + liSig, liBody, liSecret},
		"signature without prefix": {
			// HMAC of the bare body is not what LinkedIn sends.
			"5d7c6b6e2fd2d0c4ad3d0b8a3c4b0e0a1f0e8f6b6b0d7f3a2c1e0d9c8b7a6f5e", liBody, liSecret,
		},
	} {
		if core.VerifyLinkedInSignature(tc.header, []byte(tc.body), []byte(tc.secret)) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLinkedInChallengeResponse(t *testing.T) {
	if got := core.LinkedInChallengeResponse(liChallenge, []byte(liSecret)); got != liResponse {
		t.Fatalf("challengeResponse = %s, want %s", got, liResponse)
	}
}

// Meta echoes hub.challenge back verbatim; only short numeric values are
// echoed so the endpoint cannot be used to reflect arbitrary content.
func TestValidHubChallenge(t *testing.T) {
	for _, ok := range []string{"1", "1158201444", strings.Repeat("9", 32)} {
		if !core.ValidHubChallenge(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", strings.Repeat("9", 33), "12a", "-1", " 1", "<script>", "１２"} {
		if core.ValidHubChallenge(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestVerifyToken(t *testing.T) {
	if !core.VerifyToken("s3cret-verify", "s3cret-verify") {
		t.Fatal("matching verify token rejected")
	}
	for _, tc := range [][2]string{{"s3cret-verify", "s3cret-verifx"}, {"", ""}, {"s3cret", ""}, {"", "s3cret"}, {"s3cret-verify", "s3cret-verify "}} {
		if core.VerifyToken(tc[0], tc[1]) {
			t.Errorf("VerifyToken(%q, %q) accepted", tc[0], tc[1])
		}
	}
}
