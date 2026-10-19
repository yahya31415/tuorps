// Package httpx holds the HTTP plumbing shared by every handler: JSON
// responses, the canonical error shape, strict request decoding, middleware
// and hardened server/client constructors.
package httpx

import (
	"encoding/json"
	"net/http"
)

const contentTypeJSON = "application/json; charset=utf-8"

// ErrorBody is the canonical error response: {"error": {...}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code, a human message and
// the request ID for support. It never carries internal error text.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON encodes v with status. The body is marshalled before anything is
// written, so an encoding failure yields a clean 500 instead of a torn body.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal error"}}`))
		return err
	}
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteError writes the canonical error body, tagged with the request ID.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	_ = WriteJSON(w, status, ErrorBody{Error: ErrorDetail{
		Code:      code,
		Message:   message,
		RequestID: RequestIDFrom(r.Context()),
	}})
}
