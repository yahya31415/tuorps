package wshttp_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	ratecore "github.com/yahya31415/tuorps/internal/platform/ratelimit/core"
	wshttp "github.com/yahya31415/tuorps/internal/workspace/http"
)

// The workspace middleware is shared with other domains' handlers
// (connections in M3); its database path is covered end to end by
// TestWorkspaceAPI. These cases need no database.

func reached(called *bool) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *called = true })
}

func TestPrincipalFromEmptyContext(t *testing.T) {
	if _, ok := wshttp.PrincipalFrom(context.Background()); ok {
		t.Fatal("principal found in a context that has none")
	}
}

func TestWithWorkspaceRejectsMalformedWorkspaceAsNotFound(t *testing.T) {
	var called bool
	h := wshttp.WithWorkspace(nil, slog.New(slog.DiscardHandler))(reached(&called))
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/not-a-uuid", nil)
	r.SetPathValue("ws", "not-a-uuid")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || called {
		t.Fatalf("status = %d, next called = %v; want 404 and not called", w.Code, called)
	}
}

func TestLimitWritesOnlyCountsMutations(t *testing.T) {
	var counted int
	full := func(context.Context, string, time.Duration, time.Time) (ratecore.Counts, error) {
		counted++
		return ratecore.Counts{Curr: 1000}, nil
	}
	limit, err := ratecore.NewLimit(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	limiter := ratelimit.NewLimiter(full, clock.Fixed(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	mw := wshttp.LimitWrites(limiter, ratelimit.Rule{Class: "write_user", Limit: limit})

	var called bool
	w := httptest.NewRecorder()
	mw(reached(&called)).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !called || counted != 0 {
		t.Fatalf("GET: next called = %v, counted = %d; want called and not counted", called, counted)
	}

	called = false
	w = httptest.NewRecorder()
	mw(reached(&called)).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/x", nil))
	if w.Code != http.StatusTooManyRequests || called || counted != 1 {
		t.Fatalf("POST over the limit: status = %d, next called = %v, counted = %d", w.Code, called, counted)
	}
}
