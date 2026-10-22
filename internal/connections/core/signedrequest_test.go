package core_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Vectors generated independently with Python's hmac and base64 modules,
// keyed with metaSecret (webhook_test.go).
const (
	signedRequest      = "e_gGiHR_EUhFZqDUnwpwAUvGgo1lArX6h6VN4nWShng.eyJhbGdvcml0aG0iOiJITUFDLVNIQTI1NiIsImV4cGlyZXMiOjE3OTIwMDAwMDAsImlzc3VlZF9hdCI6MTc5MTk5NjQwMCwidXNlcl9pZCI6IjEwMjI4Mzc0NjU1NTY2In0"
	signedRequestSHA1  = "m7vLKBofOdvMKBW_fchBXR6wS9Z9SI-JC5odPTnctF8.eyJhbGdvcml0aG0iOiJITUFDLVNIQTEiLCJpc3N1ZWRfYXQiOjE3OTE5OTY0MDAsInVzZXJfaWQiOiIxIn0"
	signedRequestNoUID = "exwY9w1am45lEpx1VglXwIphO--uejcc-nJBRBbDIYU.eyJhbGdvcml0aG0iOiJITUFDLVNIQTI1NiIsImlzc3VlZF9hdCI6MTc5MTk5NjQwMH0"
)

func TestParseSignedRequest(t *testing.T) {
	sr, err := core.ParseSignedRequest(signedRequest, []byte(metaSecret))
	if err != nil || sr.UserID != "10228374655566" || sr.IssuedAt != 1791996400 {
		t.Fatalf("ParseSignedRequest = %+v, %v", sr, err)
	}
	if _, err := core.ParseSignedRequest(signedRequest, []byte("instagram-app-secret"), []byte(metaSecret)); err != nil {
		t.Errorf("rejected when the signing secret is the second candidate: %v", err)
	}
	sig, payload, _ := strings.Cut(signedRequest, ".")
	flipped := payload[:len(payload)-1] + map[bool]string{true: "A", false: "B"}[payload[len(payload)-1] != 'A']
	for name, tc := range map[string]struct {
		in      string
		secrets [][]byte
	}{
		"wrong secret":        {signedRequest, [][]byte{[]byte("other")}},
		"no secrets":          {signedRequest, nil},
		"tampered payload":    {sig + "." + flipped, [][]byte{[]byte(metaSecret)}},
		"swapped parts":       {payload + "." + sig, [][]byte{[]byte(metaSecret)}},
		"sha1 algorithm":      {signedRequestSHA1, [][]byte{[]byte(metaSecret)}},
		"no user_id":          {signedRequestNoUID, [][]byte{[]byte(metaSecret)}},
		"empty":               {"", [][]byte{[]byte(metaSecret)}},
		"no separator":        {sig + payload, [][]byte{[]byte(metaSecret)}},
		"extra segment":       {signedRequest + ".x", [][]byte{[]byte(metaSecret)}},
		"padded standard b64": {strings.ReplaceAll(signedRequest, "_", "/") + "=", [][]byte{[]byte(metaSecret)}},
		"signature only":      {sig + ".", [][]byte{[]byte(metaSecret)}},
	} {
		if _, err := core.ParseSignedRequest(tc.in, tc.secrets...); !errors.Is(err, core.ErrInvalidSignedRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidSignedRequest", name, err)
		}
	}
}

// Whatever the input, parsing never panics and never accepts a request
// that is not the known-good one under a secret it was not signed with.
func FuzzParseSignedRequest(f *testing.F) {
	for _, s := range []string{signedRequest, signedRequestSHA1, signedRequestNoUID, "", ".", "a.b", "a.b.c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		sr, err := core.ParseSignedRequest(in, []byte(metaSecret))
		if err != nil {
			if !errors.Is(err, core.ErrInvalidSignedRequest) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		if sr.UserID == "" {
			t.Fatalf("accepted %q without a user_id", in)
		}
		if _, err := core.ParseSignedRequest(in, []byte("a different secret")); err == nil {
			t.Fatalf("%q verifies under two different secrets", in)
		}
	})
}
