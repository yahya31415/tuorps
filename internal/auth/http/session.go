package authhttp

import (
	"context"
	"crypto/sha256"
	"net/http"
	"uuid"

	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// cookieName's __Host- prefix makes browsers require Secure, Path=/ and no
// Domain, so the cookie cannot be set or shadowed by a sibling subdomain.
// Browsers treat http://localhost as secure, so development works too.
const cookieName = "__Host-tuorps_session"

// SessionCookie is the session cookie for token (maxAge < 0 deletes it).
func SessionCookie(token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// Identity is the authenticated caller.
type Identity struct {
	UserID      uuid.UUID
	MFAVerified bool
	token       string
}

type identityKey struct{}

// IdentityFrom returns the caller set by RequireSession.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// sessionToken reads the session cookie, if any.
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// SessionHash is the SHA-256 of the caller's session token: it binds
// short-lived flows (OAuth) to one session without storing the token.
func (id Identity) SessionHash() [32]byte { return sha256.Sum256([]byte(id.token)) }

// LookupSession returns the caller of r, if r carries a live session. It
// writes nothing, for handlers that must answer failures their own way.
func LookupSession(r *http.Request, sessions auth.SessionStore) (Identity, bool, error) {
	tok := sessionToken(r)
	s, ok, err := sessions.Get(r.Context(), tok)
	if err != nil || !ok {
		return Identity{}, false, err
	}
	return Identity{UserID: s.UserID, MFAVerified: s.MFAVerified, token: tok}, true, nil
}

// RequireSession admits requests with a live session and attaches the
// caller's Identity; others get 401.
func RequireSession(sessions auth.SessionStore) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok, err := LookupSession(r, sessions)
			if err != nil {
				httpx.WriteError(w, r, http.StatusServiceUnavailable, "session_store_unavailable", "please retry shortly")
				return
			}
			if !ok {
				httpx.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "sign in to continue")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
		})
	}
}

// startSession rotates any existing session into a new one and sets the cookie.
func (h *handlers) startSession(w http.ResponseWriter, r *http.Request, user uuid.UUID, mfa bool) error {
	if old := sessionToken(r); old != "" {
		if err := h.Sessions.Delete(r.Context(), old); err != nil {
			return err
		}
	}
	tok, err := h.Sessions.Create(r.Context(), user, mfa)
	if err != nil {
		return err
	}
	http.SetCookie(w, SessionCookie(tok, int(h.SessionLife.Seconds())))
	return nil
}

func (h *handlers) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, SessionCookie("", -1))
}
