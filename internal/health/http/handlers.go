// Package healthhttp exposes liveness (/healthz) and readiness (/readyz)
// probes. Handlers are thin: run checks, evaluate in core, encode.
package healthhttp

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/yahya31415/tuorps/internal/health"
	"github.com/yahya31415/tuorps/internal/health/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// Register mounts the probes on rt. Readiness runs checks with a
// per-check timeout; failure details go to the log, never the response.
func Register(rt *httpx.Router, checks []health.NamedCheck, timeout time.Duration, log *slog.Logger) {
	rt.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	rt.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		results := health.RunChecks(r.Context(), timeout, checks)
		for _, res := range results {
			if res.Err != nil {
				log.WarnContext(r.Context(), "readiness check failed",
					"check", res.Name, "critical", res.Critical, "err", res.Err.Error())
			}
		}
		report := core.Evaluate(results)
		_ = httpx.WriteJSON(w, report.HTTPStatus(), report)
	})
}
