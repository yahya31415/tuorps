package httpx

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// Decoding failures. Each maps to one status and error code; none carries
// request content.
var (
	ErrUnsupportedMediaType = errors.New("content type must be application/json")
	ErrEmptyBody            = errors.New("request body is empty")
	ErrInvalidJSON          = errors.New("request body is not valid JSON for this endpoint")
	ErrBodyTooLarge         = errors.New("request body is too large")
	ErrValidation           = errors.New("request failed validation")
)

// Validator is implemented by request types that check their own invariants.
type Validator interface{ Validate() error }

// ValidationError carries a validator's message, which is safe to return.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string        { return e.Err.Error() }
func (e *ValidationError) Unwrap() error        { return e.Err }
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// DecodeJSON reads exactly one JSON value of type T from r, at most maxBytes
// long. It rejects other media types, unknown or duplicate members,
// case-mismatched names, invalid UTF-8 and trailing data, then runs
// T.Validate when T implements Validator.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request, maxBytes int64) (T, error) {
	var v T
	if !isJSON(r.Header.Get("Content-Type")) {
		return v, ErrUnsupportedMediaType
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return v, ErrBodyTooLarge
		}
		return v, ErrInvalidJSON
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return v, ErrEmptyBody
	}
	if err := json.Unmarshal(body, &v, json.RejectUnknownMembers(true)); err != nil {
		return v, ErrInvalidJSON
	}
	if val, ok := any(v).(Validator); ok {
		if err := val.Validate(); err != nil {
			return v, &ValidationError{Err: err}
		}
	}
	return v, nil
}

// Decode is DecodeJSON for handlers: on failure it writes the canonical
// error response and returns false.
func Decode[T any](w http.ResponseWriter, r *http.Request, maxBytes int64) (T, bool) {
	v, err := DecodeJSON[T](w, r, maxBytes)
	if err != nil {
		status, code, msg := decodeFailure(err)
		WriteError(w, r, status, code, msg)
		return v, false
	}
	return v, true
}

func decodeFailure(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, ErrUnsupportedMediaType):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupportedMediaType.Error()
	case errors.Is(err, ErrEmptyBody):
		return http.StatusBadRequest, "empty_body", ErrEmptyBody.Error()
	case errors.Is(err, ErrBodyTooLarge):
		return http.StatusRequestEntityTooLarge, "body_too_large", ErrBodyTooLarge.Error()
	case errors.Is(err, ErrValidation):
		return http.StatusUnprocessableEntity, "validation_failed", err.Error()
	default:
		return http.StatusBadRequest, "invalid_json", ErrInvalidJSON.Error()
	}
}

func isJSON(contentType string) bool {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "application/json" {
		return false
	}
	cs, ok := params["charset"]
	return !ok || strings.EqualFold(cs, "utf-8")
}
