package logx

import "testing"

// FuzzPrecheckIsSupersetOfPatterns guards the fast path: whenever any
// redaction pattern matches, mayContainSensitive must be true.
func FuzzPrecheckIsSupersetOfPatterns(f *testing.F) {
	for _, s := range []string{
		"priya@example.com", "+919876543210", "+91 98765 43210", "9876543210", "98765-43210",
		"Bearer abc", "basic Zm9v", "eyJhbGciOi.eyJzdWIi.sig", "ya29.a0-x", "EAAGm0PX4ZCpsBAKZCZBqZB9",
		"AQXdSP_W41_UPs5ioT_t8HESyODB4FqbkJ8LrV_5mff4gPODzOYR", "postgres://u:p@h", "/v1/posts", "2026-10-19",
	} {
		f.Add(s)
	}
	r := newRedactor()
	f.Fuzz(func(t *testing.T, s string) {
		if mayContainSensitive(s) {
			return
		}
		if r.userinfo.MatchString(s) {
			t.Fatalf("precheck false but userinfo pattern matches %q", s)
		}
		for _, p := range r.patterns {
			if p.MatchString(s) {
				t.Fatalf("precheck false but %s matches %q", p, s)
			}
		}
	})
}
