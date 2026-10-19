package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids/idstest"
)

const fixedID = "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f"

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorDetail {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body httpx.ErrorBody
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("error body is not the canonical shape: %v", err)
	}
	return body.Error
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := httpx.WriteJSON(rec, http.StatusCreated, map[string]string{"id": "p1"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"p1"}` {
		t.Errorf("body = %s", got)
	}
}

func TestWriteJSONReportsUnencodableValues(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := httpx.WriteJSON(rec, http.StatusOK, map[string]any{"f": func() {}}); err == nil {
		t.Fatal("want encode error")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when encoding fails", rec.Code)
	}
}

func TestWriteErrorUsesCanonicalShapeWithRequestID(t *testing.T) {
	h := httpx.RequestID(idstest.Sequence(t, uuid.MustParse(fixedID)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusConflict, "post_locked", "post is being edited")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d", rec.Code)
	}
	got := decodeError(t, rec)
	want := httpx.ErrorDetail{Code: "post_locked", Message: "post is being edited", RequestID: fixedID}
	if got != want {
		t.Errorf("error = %+v, want %+v", got, want)
	}
}

func TestRequestID(t *testing.T) {
	cases := []struct {
		name     string
		incoming string
		want     string
	}{
		{"generated when absent", "", fixedID},
		{"valid incoming UUID kept", "0199c3f0-0000-7000-8000-000000000001", "0199c3f0-0000-7000-8000-000000000001"},
		{"incoming normalised", "0199C3F0-0000-7000-8000-000000000001", "0199c3f0-0000-7000-8000-000000000001"},
		{"non-UUID replaced", "<script>alert(1)</script>", fixedID},
		{"oversized replaced", strings.Repeat("a", 4096), fixedID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			h := httpx.RequestID(idstest.Sequence(t, uuid.MustParse(fixedID)))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = httpx.RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.incoming != "" {
				req.Header.Set("X-Request-ID", tc.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if seen != tc.want {
				t.Errorf("context request ID = %q, want %q", seen, tc.want)
			}
			if got := rec.Header().Get("X-Request-ID"); got != tc.want {
				t.Errorf("response header = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	if got := httpx.RequestIDFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context()); got != "" {
		t.Errorf("RequestIDFrom(empty) = %q", got)
	}
}

func TestChainAppliesOutermostFirst(t *testing.T) {
	var order []string
	mw := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }), mw("a"), mw("b"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := strings.Join(order, ","); got != "a,b,handler" {
		t.Errorf("order = %s", got)
	}
}
