package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

func lookupFrom(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

func noFiles(string) ([]byte, error) { return nil, fs.ErrNotExist }

// testKeys is a valid keyring document with one key, "k1".
const testKeys = "k1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

// syncBuffer is a goroutine-safe log sink.
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
	err := run(t.Context(), []string{"frobnicate"}, lookupFrom(nil), noFiles, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunFailsFastOnMissingConfig(t *testing.T) {
	cases := map[string][]string{
		"serve":      {"DATABASE_URL", "REDIS_URL"},
		"migrate":    {"MIGRATE_DATABASE_URL"},
		"migrate up": {"MIGRATE_DATABASE_URL"},
		"":           {"DATABASE_URL", "REDIS_URL"},
	}
	for args, want := range cases {
		t.Run(args, func(t *testing.T) {
			err := run(t.Context(), strings.Fields(args), lookupFrom(nil), noFiles, new(bytes.Buffer))
			for _, key := range want {
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("err = %v, want mention of %s", err, key)
				}
			}
		})
	}
}

func TestRunMigrateRejectsUnknownAction(t *testing.T) {
	env := lookupFrom(map[string]string{"MIGRATE_DATABASE_URL": "postgres://u:p@127.0.0.1:1/db"})
	if err := run(t.Context(), []string{"migrate", "sideways"}, env, noFiles, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthcheckSubcommand(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	defer healthy.Close()
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer sick.Close()

	hostPort := func(u string) string { return strings.TrimPrefix(u, "http://") }
	_, port, _ := net.SplitHostPort(hostPort(healthy.URL))

	cases := []struct {
		name, addr string
		wantErr    bool
	}{
		{"healthy", hostPort(healthy.URL), false},
		{"wildcard host probes loopback", ":" + port, false},
		{"unhealthy", hostPort(sick.URL), true},
		{"nothing listening", freeAddr(t), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(t.Context(), []string{"healthcheck"}, lookupFrom(map[string]string{"HTTP_ADDR": tc.addr}), noFiles, new(bytes.Buffer))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestServeAndMigrateEndToEnd(t *testing.T) {
	pg := testinfra.StartPostgres(t)
	rd := testinfra.StartRedis(t)

	t.Run("migrate up, status, down", func(t *testing.T) {
		env := lookupFrom(map[string]string{"MIGRATE_DATABASE_URL": pg.MigratorURL})
		var out bytes.Buffer
		for _, action := range []string{"up", "status", "down", "up"} {
			if err := run(t.Context(), []string{"migrate", action}, env, noFiles, &out); err != nil {
				t.Fatalf("migrate %s: %v\n%s", action, err, out.String())
			}
		}
		if !strings.Contains(out.String(), "00001_tenant_context.sql") {
			t.Errorf("status output missing migration: %s", out.String())
		}
		if strings.Contains(out.String(), pg.MigratorURL) {
			t.Error("migration output leaks the database URL")
		}
		admin, err := pgx.Connect(t.Context(), pg.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close(context.Background()) }()
		var riverJob *string
		if err := admin.QueryRow(t.Context(), "SELECT to_regclass('tuorps.river_job')::text").Scan(&riverJob); err != nil || riverJob == nil {
			t.Errorf("migrate up did not create River's tables in schema tuorps (%v)", err)
		}
	})

	t.Run("serve answers probes then shuts down gracefully", func(t *testing.T) {
		addr := freeAddr(t)
		env := lookupFrom(map[string]string{
			"DATABASE_URL":             pg.AppURL,
			"ENCRYPTION_KEYS":          testKeys,
			"ENCRYPTION_ACTIVE_KEY_ID": "k1",
			"REDIS_URL":                rd.URL,
			"HTTP_ADDR":                addr,
			"SHUTDOWN_TIMEOUT":         "5s",
		})
		ctx, cancel := context.WithCancel(t.Context())
		logs := &syncBuffer{}
		done := make(chan error, 1)
		go func() { done <- run(ctx, []string{"serve"}, env, noFiles, logs) }()

		base := "http://" + addr
		waitUntilUp(t, base+"/healthz")

		resp := get(t, base+"/readyz")
		var report struct {
			Ready bool `json:"ready"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil || resp.StatusCode != http.StatusOK || !report.Ready {
			t.Errorf("/readyz = %d ready=%v err=%v", resp.StatusCode, report.Ready, err)
		}
		_ = resp.Body.Close()
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Request-ID") == "" {
			t.Errorf("middleware chain missing: %v", resp.Header)
		}

		resp = get(t, base+"/v1/me")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("/v1/me without a session = %d, want 401 (auth routes mounted)", resp.StatusCode)
		}
		_ = resp.Body.Close()

		resp = get(t, base+"/nope")
		if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("/nope = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		_ = resp.Body.Close()

		// Drop the client's pooled connections (including any spare it dialed
		// but never used) so the drain below is fully graceful.
		http.DefaultClient.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serve returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not shut down")
		}
		for _, want := range []string{`"msg":"http request"`, `"msg":"listening"`, `"msg":"shutdown complete"`} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("logs missing %s:\n%s", want, logs.String())
			}
		}
		if strings.Contains(logs.String(), pg.AppURL) {
			t.Error("logs leak the database URL")
		}
	})

	t.Run("serve fails fast when a dependency is unreachable", func(t *testing.T) {
		env := lookupFrom(map[string]string{"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "REDIS_URL": rd.URL, "HTTP_ADDR": freeAddr(t),
			"ENCRYPTION_KEYS": testKeys, "ENCRYPTION_ACTIVE_KEY_ID": "k1"})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if err := run(ctx, []string{"serve"}, env, noFiles, new(bytes.Buffer)); err == nil {
			t.Fatal("serve started without a database")
		}
	})
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func waitUntilUp(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became healthy", url)
}
