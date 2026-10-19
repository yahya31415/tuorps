package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/ids"
)

// Middleware decorates a handler.
type Middleware func(http.Handler) http.Handler

// Chain wraps h so that mws[0] is the outermost middleware.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// HeaderRequestID carries the request ID in both directions.
const HeaderRequestID = "X-Request-ID"

type requestIDKey struct{}

// RequestID tags each request with an ID: an incoming X-Request-ID is kept
// only if it is a UUID (normalised), otherwise newID supplies one. The ID
// is echoed in the response header and available via RequestIDFrom.
func RequestID(newID ids.NewID) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.Header.Get(HeaderRequestID))
			if err != nil {
				id = newID()
			}
			s := id.String()
			w.Header().Set(HeaderRequestID, s)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, s)))
		})
	}
}

// RequestIDFrom returns the request ID stored by RequestID, or "".
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// Recover converts a handler panic into a logged error and, if nothing has
// been written yet, a canonical 500. http.ErrAbortHandler is re-raised so
// net/http can abort the connection as intended.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &responseRecorder{ResponseWriter: w}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.ErrorContext(r.Context(), "handler panic",
					"request_id", RequestIDFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(p),
					"stack", string(debug.Stack()))
				if !rw.wroteHeader {
					WriteError(w, r, http.StatusInternalServerError, "internal", "internal error")
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// BodyLimit caps request bodies at maxBytes: declared lengths above the
// limit are refused outright, and reads past it fail (http.MaxBytesReader).
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				WriteError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", ErrBodyTooLarge.Error())
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets hardening headers for a JSON API. HSTS is only sent
// when hsts is true (production behind TLS).
func SecurityHeaders(hsts bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog writes one line per request: method, path (never the query
// string), status, bytes, duration and request ID. 4xx log at WARN and 5xx
// at ERROR.
func AccessLog(log *slog.Logger, now clock.Now) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := now()
			rw := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(rw, r)
			status := rw.statusOr200()
			id := RequestIDFrom(r.Context())
			if id == "" {
				id = w.Header().Get(HeaderRequestID)
			}
			log.LogAttrs(r.Context(), levelFor(status), "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("bytes", rw.bytes),
				slog.Int64("duration_ms", now().Sub(start).Milliseconds()),
				slog.String("request_id", id))
		})
	}
}

func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// responseRecorder tracks status and size; Unwrap keeps
// http.ResponseController working through it.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (rw *responseRecorder) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status, rw.wroteHeader = code, true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseRecorder) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.status, rw.wroteHeader = http.StatusOK, true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}

func (rw *responseRecorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseRecorder) statusOr200() int {
	if rw.status == 0 {
		return http.StatusOK
	}
	return rw.status
}
