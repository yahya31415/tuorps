package audit

import (
	"net/http"

	"github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// MetaFromRequest is the audit metadata for a request: its request ID and
// the keyed hash of the resolved client address (never the address itself).
func MetaFromRequest(r *http.Request, ip clientip.Resolver, key []byte) core.Meta {
	return core.Meta{
		RequestID: httpx.RequestIDFrom(r.Context()),
		IPHash:    core.HashIP(key, ip.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))),
	}
}
