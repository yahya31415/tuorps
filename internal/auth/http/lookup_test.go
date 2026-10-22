package authhttp_test

import (
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"uuid"

	"github.com/yahya31415/tuorps/internal/auth"
	authcore "github.com/yahya31415/tuorps/internal/auth/core"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// The OAuth callback must end every failure in a redirect, so it looks the
// session up itself; and it binds a flow to the session that started it
// through a hash of the session token, never the token (ADR 0008).
func TestLookupSessionAndSessionHash(t *testing.T) {
	rd := testinfra.StartRedis(t)
	rdb, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(rd.URL), PoolSize: 5, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	store := auth.NewSessionStore(rdb, authcore.SessionPolicy{Idle: time.Hour, Absolute: 24 * time.Hour}, clock.System(), rand.Reader)
	user := uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1")
	token, err := store.Create(t.Context(), user, true)
	if err != nil {
		t.Fatal(err)
	}
	withCookie := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/oauth/meta/callback", nil)
		if v != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-tuorps_session", Value: v})
		}
		return r
	}

	id, ok, err := authhttp.LookupSession(withCookie(token), store)
	if err != nil || !ok || id.UserID != user || !id.MFAVerified {
		t.Fatalf("LookupSession = %+v, %v, %v", id, ok, err)
	}
	if id.SessionHash() != sha256.Sum256([]byte(token)) {
		t.Error("SessionHash is not the SHA-256 of the session token")
	}
	for name, cookie := range map[string]string{"no cookie": "", "unknown session": "bm90LWEtc2Vzc2lvbi10b2tlbi1hdC1hbGwtMTIzNDU2Nzg5MA"} {
		if _, ok, err := authhttp.LookupSession(withCookie(cookie), store); ok || err != nil {
			t.Errorf("%s: ok = %v, err = %v", name, ok, err)
		}
	}
	// RequireSession attaches the same identity.
	var seen authhttp.Identity
	h := authhttp.RequireSession(store)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen, _ = authhttp.IdentityFrom(r.Context()) }))
	h.ServeHTTP(httptest.NewRecorder(), withCookie(token))
	if seen.SessionHash() != id.SessionHash() || seen.UserID != user {
		t.Errorf("RequireSession identity = %+v", seen)
	}
}
