// Package lifecycle runs servers until their context ends and probes them
// from inside the container (distroless images have no shell or curl).
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// Serve listens on srv.Addr and serves until ctx is done, then shuts down
// gracefully within shutdownTimeout, letting in-flight requests finish.
func Serve(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration, log *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("lifecycle: listen: %w", err)
	}
	log.InfoContext(ctx, "listening", "addr", ln.Addr().String())

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("lifecycle: serve: %w", err)
	case <-ctx.Done():
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	log.InfoContext(sctx, "shutting down", "timeout", shutdownTimeout.String())
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("lifecycle: shutdown: %w", err)
	}
	log.InfoContext(sctx, "shutdown complete")
	return nil
}

// Probe GETs path on the server bound to listenAddr and requires a 200. A
// wildcard host (":8080", "0.0.0.0:8080", "[::]:8080") is probed on loopback.
func Probe(ctx context.Context, listenAddr, path string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("lifecycle: probe address: %w", err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+path, nil)
	if err != nil {
		return fmt.Errorf("lifecycle: probe request: %w", err)
	}
	resp, err := httpx.NewClient(timeout).Do(req)
	if err != nil {
		return fmt.Errorf("lifecycle: probe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lifecycle: probe: status %d", resp.StatusCode)
	}
	return nil
}
