// Package httpxtest provides test helpers for httpx-based handlers.
package httpxtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// FuzzDecode fuzzes httpx.Decode for request type T: every body is either
// accepted (and then passes T's Validate, if any) or rejected with a
// documented status and error code.
func FuzzDecode[T any](f *testing.F, seeds ...string) {
	f.Helper()
	for _, s := range seeds {
		f.Add(s)
	}
	allowed := map[int][]string{
		http.StatusBadRequest:            {"empty_body", "invalid_json"},
		http.StatusRequestEntityTooLarge: {"body_too_large"},
		http.StatusUnprocessableEntity:   {"validation_failed"},
	}
	f.Fuzz(func(t *testing.T, body string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		v, ok := httpx.Decode[T](rec, req, 16<<10)
		if ok {
			if val, isVal := any(v).(httpx.Validator); isVal && val.Validate() != nil {
				t.Fatalf("accepted an invalid %T", v)
			}
			return
		}
		codes, known := allowed[rec.Code]
		if !known {
			t.Fatalf("status %d for %q", rec.Code, body)
		}
		var e httpx.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatalf("non-canonical error body: %s", rec.Body)
		}
		for _, c := range codes {
			if e.Error.Code == c {
				return
			}
		}
		t.Fatalf("status %d with code %q", rec.Code, e.Error.Code)
	})
}
