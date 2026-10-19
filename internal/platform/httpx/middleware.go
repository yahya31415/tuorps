package httpx

import (
	"context"
	"net/http"
	"uuid"

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
