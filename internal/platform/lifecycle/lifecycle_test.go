package lifecycle_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/lifecycle"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// start runs Serve in the background and waits until it accepts connections.
func start(t *testing.T, h http.Handler, shutdown time.Duration) (addr string, logs *syncBuffer, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	addr = freeAddr(t)
	logs = &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: time.Second}
	go func() { errc <- lifecycle.Serve(ctx, srv, shutdown, slog.New(slog.NewJSONHandler(logs, nil))) }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, logs, cancel, errc
}

func waitDone(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("Serve did not return within %v", within)
		return nil
	}
}

func TestServeShutsDownCleanlyWhenIdle(t *testing.T) {
	_, logs, cancel, done := start(t, http.NotFoundHandler(), time.Second)
	cancel()
	if err := waitDone(t, done, 3*time.Second); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	if !strings.Contains(logs.String(), "shutdown complete") {
		t.Errorf("logs: %s", logs)
	}
}

// Regression: a client connection that never sends a request (Go's
// transport leaves these after dial races) is only "idle" to Shutdown after
// 5s, so a shorter drain timed out and Serve returned an error.
func TestServeForcesCloseWhenDrainTimesOut(t *testing.T) {
	addr, logs, cancel, done := start(t, http.NotFoundHandler(), 200*time.Millisecond)
	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	time.Sleep(50 * time.Millisecond) // let the server accept it

	cancel()
	if err := waitDone(t, done, 3*time.Second); err != nil {
		t.Fatalf("Serve = %v, want nil after forcing close", err)
	}
	if !strings.Contains(logs.String(), "forced close") {
		t.Errorf("forced close not logged: %s", logs)
	}
}

func TestServeForcesCloseOfStuckRequests(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stuck := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release })
	addr, _, cancel, done := start(t, stuck, 200*time.Millisecond)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	cancel()
	if err := waitDone(t, done, 3*time.Second); err != nil {
		t.Fatalf("Serve = %v", err)
	}
}

func TestServeReportsListenErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{Addr: ln.Addr().String(), ReadHeaderTimeout: time.Second}
	if err := lifecycle.Serve(t.Context(), srv, time.Second, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("Serve on a taken port returned nil")
	}
}

func TestProbeRejectsBadAddress(t *testing.T) {
	if err := lifecycle.Probe(t.Context(), "no-port", "/healthz", time.Second); err == nil {
		t.Fatal("want error")
	}
}
