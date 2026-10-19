package main

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func lookupFrom(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

func noFiles(string) ([]byte, error) { return nil, fs.ErrNotExist }

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

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	if err := run(t.Context(), []string{"migrate"}, lookupFrom(nil), noFiles, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunFailsFastOnMissingConfig(t *testing.T) {
	err := run(t.Context(), nil, lookupFrom(nil), noFiles, new(bytes.Buffer))
	for _, key := range []string{"DATABASE_URL", "REDIS_URL"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("err = %v, want mention of %s", err, key)
		}
	}
}

func TestHealthcheckWithNothingListening(t *testing.T) {
	err := run(t.Context(), []string{"healthcheck"}, lookupFrom(map[string]string{"WORKER_HEALTH_ADDR": freeAddr(t)}), noFiles, new(bytes.Buffer))
	if err == nil {
		t.Fatal("healthcheck passed with no worker running")
	}
}

func TestWorkerServesHealthAndShutsDown(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	rd := testinfra.StartRedis(t)
	addr := freeAddr(t)
	env := lookupFrom(map[string]string{
		"DATABASE_URL":             pg.AppURL,
		"ENCRYPTION_KEYS":          "k1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		"ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"REDIS_URL":                rd.URL,
		"WORKER_HEALTH_ADDR":       addr,
		"HTTP_ADDR":                freeAddr(t),
	})
	ctx, cancel := context.WithCancel(t.Context())
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}, env, noFiles, logs) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := run(t.Context(), []string{"healthcheck"}, env, noFiles, new(bytes.Buffer)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker never became healthy:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := http.Get("http://" + addr + "/readyz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz: %v %v", resp, err)
	}
	_ = resp.Body.Close()

	http.DefaultClient.CloseIdleConnections() // no spare connections to wait out
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not shut down")
	}
	if !strings.Contains(logs.String(), `"component":"worker"`) {
		t.Errorf("worker logs not tagged:\n%s", logs.String())
	}
}
