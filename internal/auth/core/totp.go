package core

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP is HMAC-SHA1 (authenticator apps require it); HMAC does not rely on collision resistance
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30 // seconds
	totpDigits = 6
	totpSkew   = 1 // steps accepted either side of now
)

// TOTPStep is the RFC 6238 time step containing t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// TOTPCode is the six-digit HOTP value (RFC 4226) of secret at counter.
func TOTPCode(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	h := mac.Sum(nil)
	off := h[len(h)-1] & 0x0f
	bin := binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1_000_000)
}

// VerifyTOTP checks code against the steps around now. A step at or before
// lastUsedStep is rejected, so each code works once. It returns the step
// that matched, to be stored as the new lastUsedStep.
func VerifyTOTP(secret []byte, code string, now time.Time, lastUsedStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits || strings.ContainsFunc(code, func(r rune) bool { return r < '0' || r > '9' }) {
		return 0, false
	}
	cur := TOTPStep(now)
	matched, ok := int64(0), false
	for s := cur - totpSkew; s <= cur+totpSkew; s++ {
		if s < 0 || s <= lastUsedStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, uint64(s))), []byte(code)) == 1 && !ok {
			matched, ok = s, true
		}
	}
	return matched, ok
}

// EncodeTOTPSecret renders secret as unpadded base32, as authenticator apps expect.
func EncodeTOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}

// OTPAuthURI is the provisioning URI rendered as a QR code by the client.
func OTPAuthURI(issuer, account string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return (&url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + issuer + ":" + account, RawQuery: q.Encode()}).String()
}
