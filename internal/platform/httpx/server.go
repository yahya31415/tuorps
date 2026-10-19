package httpx

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
)

const (
	maxHeaderBytes = 64 << 10
	maxRedirects   = 3
)

// NewServer returns an http.Server with every timeout set from cfg and the
// server's own error log routed through log.
func NewServer(addr string, h http.Handler, cfg config.HTTP, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// NewClient returns an outbound client bounded by timeout end to end, with
// dial, TLS and header timeouts, TLS 1.2+, and redirects limited to a few
// hops that never downgrade from https to http.
func NewClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("httpx: too many redirects")
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("httpx: refusing https to http redirect")
			}
			return nil
		},
	}
}

// Router is a ServeMux that answers unmatched paths and methods with the
// canonical JSON error and remembers its patterns (for the OpenAPI check).
type Router struct {
	mux      *http.ServeMux
	patterns []string
}

// NewRouter returns an empty Router.
func NewRouter() *Router { return &Router{mux: http.NewServeMux()} }

// Handle registers h for pattern (Go 1.22+ pattern syntax).
func (rt *Router) Handle(pattern string, h http.Handler) {
	rt.mux.Handle(pattern, h)
	rt.patterns = append(rt.patterns, pattern)
}

// HandleFunc registers f for pattern.
func (rt *Router) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	rt.Handle(pattern, http.HandlerFunc(f))
}

// Patterns returns the registered patterns in registration order.
func (rt *Router) Patterns() []string { return slices.Clone(rt.patterns) }

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, pattern := rt.mux.Handler(r)
	if pattern != "" {
		rt.mux.ServeHTTP(w, r)
		return
	}
	// No route: let the mux decide 404 vs 405 (and Allow), then answer in JSON.
	probe := &statusProbe{header: http.Header{}}
	h.ServeHTTP(probe, r)
	if allow := probe.header.Get("Allow"); allow != "" {
		w.Header().Set("Allow", allow)
	}
	switch probe.status {
	case http.StatusMethodNotAllowed:
		WriteError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	default:
		WriteError(w, r, http.StatusNotFound, "not_found", "not found")
	}
}

// statusProbe records the status a fallback handler would send, discarding its body.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header         { return p.header }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(code int)        { p.status = code }
