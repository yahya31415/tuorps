package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids/idstest"
)

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestRecoverTurnsPanicIntoCanonical500(t *testing.T) {
	var logs bytes.Buffer
	h := httpx.Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("db exploded: password=hunter2") }),
		httpx.RequestID(idstest.Sequence(t, uuid.MustParse(fixedID))),
		httpx.Recover(jsonLogger(&logs)),
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	got := decodeError(t, rec)
	if got.Code != "internal" || got.RequestID != fixedID {
		t.Errorf("error = %+v", got)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "exploded") {
		t.Errorf("panic detail leaked to client: %s", rec.Body)
	}
	lines := logLines(t, &logs)
	if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["request_id"] != fixedID {
		t.Errorf("panic not logged with request ID: %v", lines)
	}
}

func TestRecoverDoesNotRewriteStartedResponses(t *testing.T) {
	h := httpx.Recover(jsonLogger(new(bytes.Buffer)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted || rec.Body.String() != "partial" {
		t.Errorf("got %d %q", rec.Code, rec.Body)
	}
}

func TestRecoverRepanicsAbortHandler(t *testing.T) {
	h := httpx.Recover(jsonLogger(new(bytes.Buffer)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestBodyLimit(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "too large")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := httpx.BodyLimit(16)(echo)

	t.Run("within limit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789abcdef")))
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d", rec.Code)
		}
	})
	t.Run("declared length over limit is refused before the handler", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 17))))
		if rec.Code != http.StatusRequestEntityTooLarge || decodeError(t, rec).Code != "body_too_large" {
			t.Errorf("status = %d body = %s", rec.Code, rec.Body)
		}
	})
	t.Run("undeclared length is cut off while reading", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(strings.NewReader(strings.Repeat("x", 64))))
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for _, hsts := range []bool{false, true} {
		rec := httptest.NewRecorder()
		httpx.SecurityHeaders(hsts)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("hsts=%v %s = %q, want %q", hsts, k, got, v)
			}
		}
		gotHSTS := rec.Header().Get("Strict-Transport-Security")
		if hsts && gotHSTS != "max-age=63072000; includeSubDomains" {
			t.Errorf("HSTS = %q", gotHSTS)
		}
		if !hsts && gotHSTS != "" {
			t.Errorf("HSTS set in dev: %q", gotHSTS)
		}
	}
}

func TestAccessLog(t *testing.T) {
	ticks := []time.Time{time.Unix(100, 0), time.Unix(100, int64(42*time.Millisecond))}
	now := func() time.Time { t0 := ticks[0]; ticks = ticks[1:]; return t0 }
	var logs bytes.Buffer
	h := httpx.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("short and stout"))
		}),
		httpx.AccessLog(jsonLogger(&logs), now),
		httpx.RequestID(idstest.Sequence(t, uuid.MustParse(fixedID))),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/v1/posts/p1?token=abc", nil))

	lines := logLines(t, &logs)
	if len(lines) != 1 {
		t.Fatalf("want one access log line, got %v", lines)
	}
	l := lines[0]
	checks := map[string]any{
		"msg": "http request", "method": "PUT", "path": "/v1/posts/p1", "status": float64(418),
		"bytes": float64(15), "duration_ms": float64(42), "request_id": fixedID, "level": "WARN",
	}
	for k, v := range checks {
		if l[k] != v {
			t.Errorf("%s = %v, want %v", k, l[k], v)
		}
	}
	if strings.Contains(logs.String(), "token=abc") {
		t.Errorf("query string logged: %s", logs.String())
	}
}

func TestAccessLogLevels(t *testing.T) {
	for status, level := range map[int]string{200: "INFO", 302: "INFO", 404: "WARN", 503: "ERROR"} {
		var logs bytes.Buffer
		h := httpx.AccessLog(jsonLogger(&logs), time.Now)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if got := logLines(t, &logs)[0]["level"]; got != level {
			t.Errorf("status %d logged at %v, want %s", status, got, level)
		}
	}
}

func TestAccessLogDefaultsToStatus200AndKeepsResponseController(t *testing.T) {
	var logs bytes.Buffer
	h := httpx.AccessLog(jsonLogger(&logs), time.Now)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush through wrapper: %v", err)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := logLines(t, &logs)[0]["status"]; got != float64(200) {
		t.Errorf("status = %v", got)
	}
}
