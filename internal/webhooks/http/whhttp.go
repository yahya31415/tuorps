// Package whhttp serves the platform webhook endpoints (ADR 0010): the
// subscription handshakes, signed event deliveries, and Meta's deauthorize
// and data-deletion callbacks. There is no session: each route verifies its
// platform's signature over the raw body, with the secret chosen by the
// route, before anything is parsed. A platform without credentials has no
// endpoints (404).
package whhttp

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	ratecore "github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	"github.com/yahya31415/tuorps/internal/webhooks"
)

const (
	maxBody         = 1 << 20
	maxChallengeLen = 128
	maxCodeLen      = 64
)

// Deps are the handlers' collaborators. A nil app disables its routes.
type Deps struct {
	Inbox     webhooks.Inbox
	Meta      *config.MetaApp
	Instagram *config.InstagramApp
	LinkedIn  *config.OAuthApp
	Limiter   ratelimit.Limiter
	ClientIP  clientip.Resolver
	Origin    *url.URL // data-deletion status pages live on the browser app
	Now       clock.Now
	Rand      io.Reader
	Log       *slog.Logger
}

type handlers struct{ Deps }

// Register mounts the webhook routes on rt. Cross-origin protection must
// exempt /v1/webhooks/ (server-to-server; see app.BuildAPI).
func Register(rt *httpx.Router, d Deps) error {
	limit, err := ratecore.NewLimit(600, time.Minute)
	if err != nil {
		return err
	}
	if d.Rand == nil {
		d.Rand = rand.Reader
	}
	h := &handlers{Deps: d}
	// Fail open: a Redis outage must not lose platform deliveries.
	perIP := d.Limiter.Middleware(ratelimit.Rule{Class: "webhook_ip", Limit: limit}, func(r *http.Request) string {
		return d.ClientIP.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For")).String()
	})
	route := func(f http.HandlerFunc) http.Handler { return perIP(f) }

	rt.Handle("GET /v1/webhooks/meta", route(h.metaHandshake))
	rt.Handle("POST /v1/webhooks/meta", route(h.metaEvent))
	rt.Handle("GET /v1/webhooks/instagram", route(h.instagramHandshake))
	rt.Handle("POST /v1/webhooks/instagram", route(h.instagramEvent))
	rt.Handle("GET /v1/webhooks/linkedin", route(h.linkedInChallenge))
	rt.Handle("POST /v1/webhooks/linkedin", route(h.linkedInEvent))
	rt.Handle("POST /v1/webhooks/meta/deauthorize", route(h.signed(core.ProviderMeta, h.deauthorize)))
	rt.Handle("POST /v1/webhooks/instagram/deauthorize", route(h.signed(core.ProviderInstagram, h.deauthorize)))
	rt.Handle("POST /v1/webhooks/meta/data-deletion", route(h.signed(core.ProviderMeta, h.dataDeletion)))
	rt.Handle("POST /v1/webhooks/instagram/data-deletion", route(h.signed(core.ProviderInstagram, h.dataDeletion)))
	rt.Handle("GET /v1/data-deletion/{code}", route(h.deletionStatus))
	return nil
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusNotFound, "not_found", "not found")
}

// metaSecrets lists the secrets that may sign provider p's deliveries.
// Instagram Login accepts its own app secret or the Meta app's, because Meta
// does not document which one signs (ADR 0010).
func (h *handlers) metaSecrets(p core.Provider) [][]byte {
	var out [][]byte
	if p == core.ProviderInstagram && h.Instagram != nil {
		out = append(out, []byte(h.Instagram.ClientSecret.Reveal()))
	}
	if h.Meta != nil && (p == core.ProviderMeta || h.Instagram != nil) {
		out = append(out, []byte(h.Meta.ClientSecret.Reveal()))
	}
	return out
}

// body reads the raw request body, capped.
func body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		return nil, false
	}
	return b, true
}

func (h *handlers) ingest(w http.ResponseWriter, r *http.Request, p core.Provider, e webhooks.Event, payload []byte) {
	if _, err := h.Inbox.Ingest(r.Context(), p, e, payload, h.Now()); err != nil {
		h.Log.ErrorContext(r.Context(), "webhook ingest failed", "provider", string(p), "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "retry later")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func refuseSignature(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_signature", "the signature does not match")
}

// handshake answers Meta's subscription check: echo hub.challenge when the
// verify token matches (constant time) and the challenge is short digits.
func handshake(w http.ResponseWriter, r *http.Request, verifyToken string) {
	q := r.URL.Query()
	if q.Get("hub.mode") != "subscribe" || !core.ValidHubChallenge(q.Get("hub.challenge")) {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_handshake", "not a subscription handshake")
		return
	}
	if !core.VerifyToken(q.Get("hub.verify_token"), verifyToken) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "verify token mismatch")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
}

func (h *handlers) metaHandshake(w http.ResponseWriter, r *http.Request) {
	if h.Meta == nil {
		notFound(w, r)
		return
	}
	handshake(w, r, h.Meta.WebhookVerifyToken.Reveal())
}

func (h *handlers) instagramHandshake(w http.ResponseWriter, r *http.Request) {
	if h.Instagram == nil {
		notFound(w, r)
		return
	}
	handshake(w, r, h.Instagram.WebhookVerifyToken.Reveal())
}

func (h *handlers) metaFamilyEvent(w http.ResponseWriter, r *http.Request, p core.Provider) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	if !core.VerifyMetaSignature(r.Header.Get("X-Hub-Signature-256"), b, h.metaSecrets(p)...) {
		refuseSignature(w, r)
		return
	}
	h.ingest(w, r, p, webhooks.EventNotification, b)
}

func (h *handlers) metaEvent(w http.ResponseWriter, r *http.Request) {
	if h.Meta == nil {
		notFound(w, r)
		return
	}
	h.metaFamilyEvent(w, r, core.ProviderMeta)
}

func (h *handlers) instagramEvent(w http.ResponseWriter, r *http.Request) {
	if h.Instagram == nil {
		notFound(w, r)
		return
	}
	h.metaFamilyEvent(w, r, core.ProviderInstagram)
}

type linkedInChallenge struct {
	ChallengeCode     string `json:"challengeCode"`
	ChallengeResponse string `json:"challengeResponse"`
}

// linkedInChallenge answers LinkedIn's endpoint validation (repeated every
// two hours; due within three seconds).
func (h *handlers) linkedInChallenge(w http.ResponseWriter, r *http.Request) {
	if h.LinkedIn == nil {
		notFound(w, r)
		return
	}
	code := r.URL.Query().Get("challengeCode")
	if code == "" || len(code) > maxChallengeLen {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_handshake", "missing challengeCode")
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, linkedInChallenge{ChallengeCode: code, ChallengeResponse: core.LinkedInChallengeResponse(code, []byte(h.LinkedIn.ClientSecret.Reveal()))})
}

func (h *handlers) linkedInEvent(w http.ResponseWriter, r *http.Request) {
	if h.LinkedIn == nil {
		notFound(w, r)
		return
	}
	b, ok := body(w, r)
	if !ok {
		return
	}
	if !core.VerifyLinkedInSignature(r.Header.Get("X-LI-Signature"), b, []byte(h.LinkedIn.ClientSecret.Reveal())) {
		refuseSignature(w, r)
		return
	}
	h.ingest(w, r, core.ProviderLinkedIn, webhooks.EventNotification, b)
}

// signed verifies a Meta-style signed_request form post for p and passes
// its payload on.
func (h *handlers) signed(p core.Provider, next func(http.ResponseWriter, *http.Request, core.Provider, core.SignedRequest)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secrets := h.metaSecrets(p)
		if (p == core.ProviderInstagram && h.Instagram == nil) || (p == core.ProviderMeta && h.Meta == nil) {
			notFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_signed_request", "invalid signed_request")
			return
		}
		sr, err := core.ParseSignedRequest(r.PostForm.Get("signed_request"), secrets...)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_signed_request", "invalid signed_request")
			return
		}
		next(w, r, p, sr)
	}
}

func (h *handlers) deauthorize(w http.ResponseWriter, r *http.Request, p core.Provider, sr core.SignedRequest) {
	h.ingest(w, r, p, webhooks.EventDeauthorize, []byte(sr.UserID))
}

type deletionResponse struct {
	URL              string `json:"url"`
	ConfirmationCode string `json:"confirmation_code"`
}

// dataDeletion records the request and answers in the same response with
// the confirmation code and its status page, as Meta requires.
func (h *handlers) dataDeletion(w http.ResponseWriter, r *http.Request, p core.Provider, sr core.SignedRequest) {
	raw := make([]byte, 18)
	if _, err := io.ReadFull(h.Rand, raw); err != nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "retry later")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	if err := h.Inbox.RequestDeletion(r.Context(), p, sr.UserID, code, h.Now()); err != nil {
		h.Log.ErrorContext(r.Context(), "data deletion request failed", "provider", string(p), "err", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "retry later")
		return
	}
	status := *h.Origin
	status.Path, status.RawQuery, status.Fragment = "/data-deletion/"+code, "", ""
	_ = httpx.WriteJSON(w, http.StatusOK, deletionResponse{URL: status.String(), ConfirmationCode: code})
}

type deletionStatus struct {
	ConfirmationCode string `json:"confirmation_code"`
	Status           string `json:"status"`
}

// deletionStatus is the public status of a data-deletion request: only its
// code and state.
func (h *handlers) deletionStatus(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if code == "" || len(code) > maxCodeLen {
		notFound(w, r)
		return
	}
	status, ok, err := h.Inbox.DeletionStatus(r.Context(), code)
	switch {
	case err != nil:
		h.Log.ErrorContext(r.Context(), "data deletion status failed", "err", err.Error())
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "retry later")
	case !ok:
		notFound(w, r)
	default:
		_ = httpx.WriteJSON(w, http.StatusOK, deletionStatus{ConfirmationCode: code, Status: status})
	}
}
