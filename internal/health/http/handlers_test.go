package healthhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/health"
	"github.com/yahya31415/tuorps/internal/health/core"
	healthhttp "github.com/yahya31415/tuorps/internal/health/http"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

func serve(t *testing.T, checks []health.NamedCheck, logs *bytes.Buffer, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rt := httpx.NewRouter()
	healthhttp.Register(rt, checks, 200*time.Millisecond, slog.New(slog.NewJSONHandler(logs, nil)))
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthzIsLivenessOnly(t *testing.T) {
	var ran atomic.Bool
	checks := []health.NamedCheck{{Name: "postgres", Critical: true, Run: func(context.Context) error { ran.Store(true); return errors.New("down") }}}
	rec := serve(t, checks, new(bytes.Buffer), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body)
	}
	if ran.Load() {
		t.Error("liveness probe ran dependency checks")
	}
}

func TestReadyzAllHealthy(t *testing.T) {
	checks := []health.NamedCheck{
		{Name: "postgres", Critical: true, Run: func(context.Context) error { return nil }},
		{Name: "redis", Critical: true, Run: func(context.Context) error { return nil }},
	}
	rec := serve(t, checks, new(bytes.Buffer), http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got core.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Ready || len(got.Checks) != 2 || got.Checks[1] != (core.CheckStatus{Name: "redis", Status: core.StatusOK}) {
		t.Errorf("report = %+v", got)
	}
}

func TestReadyzFailureHidesDetailButLogsIt(t *testing.T) {
	var logs bytes.Buffer
	checks := []health.NamedCheck{
		{Name: "postgres", Critical: true, Run: func(context.Context) error { return nil }},
		{Name: "redis", Critical: true, Run: func(context.Context) error { return errors.New("dial tcp 10.0.0.7:6379: connection refused") }},
	}
	rec := serve(t, checks, &logs, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `{"name":"redis","status":"fail"}`) {
		t.Errorf("failing check not named: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "refused") {
		t.Errorf("error detail leaked to client: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), `"check":"redis"`) || !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("failure not logged: %s", logs.String())
	}
}

func TestReadyzTimesOutHungChecks(t *testing.T) {
	checks := []health.NamedCheck{{Name: "postgres", Critical: true, Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}}
	start := time.Now()
	rec := serve(t, checks, new(bytes.Buffer), http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Errorf("status %d after %v", rec.Code, time.Since(start))
	}
}

func TestProbesRejectOtherMethods(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := serve(t, nil, new(bytes.Buffer), http.MethodPost, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", path, rec.Code)
		}
	}
}

func TestProbesSupportHEAD(t *testing.T) {
	rec := serve(t, nil, new(bytes.Buffer), http.MethodHead, "/healthz")
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /healthz = %d", rec.Code)
	}
}
