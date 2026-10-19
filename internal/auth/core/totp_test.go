package core_test

import (
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/yahya31415/tuorps/internal/auth/core"
)

func rfcSecret() []byte { return []byte("12345678901234567890") } // RFC 4226/6238 SHA-1 test key

func TestHOTPMatchesRFC4226(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, w := range want {
		if got := core.TOTPCode(rfcSecret(), uint64(counter)); got != w {
			t.Errorf("counter %d: %s, want %s", counter, got, w)
		}
	}
}

func TestTOTPMatchesRFC6238(t *testing.T) {
	// RFC 6238 Appendix B (SHA-1), last six of the eight published digits.
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		step := core.TOTPStep(time.Unix(unix, 0))
		if got := core.TOTPCode(rfcSecret(), uint64(step)); got != want {
			t.Errorf("T=%d: %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := core.TOTPStep(now)
	code := func(s int64) string { return core.TOTPCode(rfcSecret(), uint64(s)) }
	cases := []struct {
		name     string
		code     string
		lastUsed int64
		wantStep int64
		ok       bool
	}{
		{"current step", code(step), 0, step, true},
		{"previous step (clock skew)", code(step - 1), 0, step - 1, true},
		{"next step (clock skew)", code(step + 1), 0, step + 1, true},
		{"two steps old", code(step - 2), 0, 0, false},
		{"replay of the last used step", code(step), step, 0, false},
		{"older than the last used step", code(step - 1), step, 0, false},
		{"newer than the last used step", code(step + 1), step, step + 1, true},
		{"five digits", code(step)[:5], 0, 0, false},
		{"seven digits", code(step) + "0", 0, 0, false},
		{"letters", "12a456", 0, 0, false},
		{"unicode digits", "١٢٣٤٥٦", 0, 0, false},
		{"spaces trimmed", " " + code(step) + " ", 0, step, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := core.VerifyTOTP(rfcSecret(), tc.code, now, tc.lastUsed)
			if ok != tc.ok || (ok && got != tc.wantStep) {
				t.Fatalf("VerifyTOTP = %d, %v; want %d, %v", got, ok, tc.wantStep, tc.ok)
			}
		})
	}
}

// Property: the current code verifies exactly once.
func TestPropertyCurrentCodeVerifiesOnce(t *testing.T) {
	prop := func(secret [20]byte, unix uint32) bool {
		now := time.Unix(int64(unix)+60, 0)
		c := core.TOTPCode(secret[:], uint64(core.TOTPStep(now)))
		step, ok := core.VerifyTOTP(secret[:], c, now, 0)
		if !ok {
			return false
		}
		_, again := core.VerifyTOTP(secret[:], c, now, step)
		return !again
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func TestOTPAuthURI(t *testing.T) {
	uri := core.OTPAuthURI("Tuorps", "priya@example.in", rfcSecret())
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("URI = %q, %v", uri, err)
	}
	if u.Path != "/Tuorps:priya@example.in" {
		t.Errorf("label = %q", u.Path)
	}
	q := u.Query()
	if q.Get("secret") != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" || q.Get("issuer") != "Tuorps" ||
		q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("query = %v", q)
	}
	if strings.Contains(q.Get("secret"), "=") {
		t.Error("secret has base32 padding")
	}
	if core.EncodeTOTPSecret(rfcSecret()) != q.Get("secret") {
		t.Error("EncodeTOTPSecret disagrees with the URI")
	}
}

func FuzzVerifyTOTP(f *testing.F) {
	f.Add("287082", int64(0))
	f.Add("", int64(-1))
	f.Fuzz(func(t *testing.T, code string, lastUsed int64) {
		now := time.Unix(1111111111, 0)
		step, ok := core.VerifyTOTP(rfcSecret(), code, now, lastUsed)
		if !ok {
			return
		}
		cur := core.TOTPStep(now)
		if step <= lastUsed || step < cur-1 || step > cur+1 {
			t.Fatalf("accepted step %d (current %d, last used %d)", step, cur, lastUsed)
		}
	})
}
