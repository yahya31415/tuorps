package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

type createPost struct {
	Caption string   `json:"caption"`
	Network []string `json:"networks"`
	Count   int      `json:"count,omitempty"`
}

func (p createPost) Validate() error {
	if p.Caption == "" {
		return errors.New("caption is required")
	}
	return nil
}

type noValidate struct {
	A int `json:"a"`
}

func jsonRequest(body, contentType string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/posts", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestDecodeAcceptsAStrictSingleObject(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON; charset=UTF-8"} {
		rec := httptest.NewRecorder()
		got, ok := httpx.Decode[createPost](rec, jsonRequest(`{"caption":"Diwali sale","networks":["instagram","facebook"]}`, ct), 1<<10)
		if !ok {
			t.Fatalf("Content-Type %q: rejected with %d %s", ct, rec.Code, rec.Body)
		}
		if got.Caption != "Diwali sale" || len(got.Network) != 2 {
			t.Errorf("decoded %+v", got)
		}
	}
}

func TestDecodeRejections(t *testing.T) {
	cases := []struct {
		name, body, contentType string
		limit                   int64
		status                  int
		code                    string
	}{
		{"missing content type", `{"caption":"x"}`, "", 1 << 10, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"form content type", `caption=x`, "application/x-www-form-urlencoded", 1 << 10, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"non-utf8 charset", `{"caption":"x"}`, "application/json; charset=latin1", 1 << 10, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"empty body", ``, "application/json", 1 << 10, http.StatusBadRequest, "empty_body"},
		{"whitespace body", "  \n", "application/json", 1 << 10, http.StatusBadRequest, "empty_body"},
		{"malformed", `{"caption":`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"unknown field", `{"caption":"x","admin":true}`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"case-mismatched field", `{"Caption":"x"}`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"duplicate key", `{"caption":"x","caption":"y"}`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"trailing value", `{"caption":"x"}{"caption":"y"}`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"wrong type", `{"caption":"x","count":"3"}`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"array instead of object", `[{"caption":"x"}]`, "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"invalid utf-8", "{\"caption\":\"\xff\"}", "application/json", 1 << 10, http.StatusBadRequest, "invalid_json"},
		{"over the limit", `{"caption":"` + strings.Repeat("x", 2048) + `"}`, "application/json", 1 << 10, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"fails validation", `{"caption":""}`, "application/json", 1 << 10, http.StatusUnprocessableEntity, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if _, ok := httpx.Decode[createPost](rec, jsonRequest(tc.body, tc.contentType), tc.limit); ok {
				t.Fatal("accepted")
			}
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := decodeError(t, rec); got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
		})
	}
}

func TestDecodeErrorsDoNotEchoTheBody(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Decode[createPost](rec, jsonRequest(`{"caption":"x","secret_zz9":"hunter2"}`, "application/json"), 1<<10)
	if body := rec.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "zz9") {
		t.Errorf("error echoes request content: %s", body)
	}
}

func TestDecodeValidationMessageIsReturned(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Decode[createPost](rec, jsonRequest(`{"caption":""}`, "application/json"), 1<<10)
	if got := decodeError(t, rec); got.Message != "caption is required" {
		t.Errorf("message = %q", got.Message)
	}
}

func TestDecodeWithoutValidator(t *testing.T) {
	rec := httptest.NewRecorder()
	got, ok := httpx.Decode[noValidate](rec, jsonRequest(`{"a":7}`, "application/json"), 1<<10)
	if !ok || got.A != 7 {
		t.Fatalf("got %+v ok=%v (%d %s)", got, ok, rec.Code, rec.Body)
	}
}

func TestDecodeJSONExposesTypedErrors(t *testing.T) {
	_, err := httpx.DecodeJSON[createPost](httptest.NewRecorder(), jsonRequest(`{`, "application/json"), 1<<10)
	if !errors.Is(err, httpx.ErrInvalidJSON) {
		t.Fatalf("err = %v, want ErrInvalidJSON", err)
	}
}

// FuzzDecode: any body either decodes into a value that passes validation,
// or is rejected with one of the documented statuses and error codes.
func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		`{"caption":"x"}`, `{"caption":"x","networks":["instagram"]}`, `{"caption":""}`, `{`, `[]`, `null`,
		`{"caption":"x"} `, `{"caption":"x"}{}`, `{"caption":1}`, "\xff", `{"caption":"x","caption":"y"}`,
	} {
		f.Add(s)
	}
	allowed := map[int]string{
		http.StatusBadRequest:            "empty_body invalid_json",
		http.StatusRequestEntityTooLarge: "body_too_large",
		http.StatusUnprocessableEntity:   "validation_failed",
	}
	f.Fuzz(func(t *testing.T, body string) {
		rec := httptest.NewRecorder()
		got, ok := httpx.Decode[createPost](rec, jsonRequest(body, "application/json"), 512)
		if ok {
			if got.Validate() != nil {
				t.Fatalf("accepted an invalid value %+v", got)
			}
			return
		}
		codes, known := allowed[rec.Code]
		if !known {
			t.Fatalf("unexpected status %d for %q", rec.Code, body)
		}
		if detail := decodeError(t, rec); !strings.Contains(codes, detail.Code) {
			t.Fatalf("status %d with code %q", rec.Code, detail.Code)
		}
	})
}
