package app_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/app"
	"github.com/yahya31415/tuorps/internal/platform/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	kv := map[string]string{
		"DATABASE_URL":             "postgres://u:p@127.0.0.1:1/db",
		"REDIS_URL":                "redis://127.0.0.1:1/0",
		"ENCRYPTION_KEYS":          "k1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		"ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"APP_ORIGIN":               "https://app.tuorps.test",
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := kv[k]; return v, ok }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// lazyDeps points at nothing; nothing here needs a live database.
func lazyDeps(t *testing.T) *app.Deps {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://u:p@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	return &app.Deps{Pool: pool, Redis: rdb}
}

func TestBuildAPIRegistersTheRoutes(t *testing.T) {
	api, err := app.BuildAPI(testConfig(t), lazyDeps(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BuildAPI: %v", err)
	}
	patterns := strings.Join(api.Router.Patterns(), "\n")
	for _, want := range []string{"GET /healthz", "GET /readyz", "POST /v1/auth/signup", "POST /v1/auth/login", "GET /v1/me"} {
		if !strings.Contains(patterns, want) {
			t.Errorf("route %q missing", want)
		}
	}
}

func TestCrossSiteWritesAreRefused(t *testing.T) {
	api, err := app.BuildAPI(testConfig(t), lazyDeps(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	send := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://api.tuorps.test/v1/auth/login", strings.NewReader(`{"email":"a@b.in","password":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		api.Handler.ServeHTTP(rec, req)
		return rec
	}

	cross := send(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d", cross.Code)
	}
	var body struct{ Error struct{ Code string } }
	b, _ := io.ReadAll(cross.Body)
	if err := json.Unmarshal(b, &body); err != nil || body.Error.Code != "cross_origin_request" {
		t.Errorf("cross-site rejection body = %s", b)
	}
	if cross.Header().Get("X-Request-ID") == "" || cross.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("rejection bypassed the standard middleware")
	}

	for name, h := range map[string]map[string]string{
		"trusted app origin":     {"Sec-Fetch-Site": "cross-site", "Origin": "https://app.tuorps.test"},
		"same origin":            {"Sec-Fetch-Site": "same-origin"},
		"non-browser API client": {},
	} {
		if rec := send(h); rec.Code == http.StatusForbidden {
			t.Errorf("%s: refused as cross-origin", name)
		}
	}

	get := httptest.NewRequest(http.MethodGet, "https://api.tuorps.test/healthz", nil)
	get.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Errorf("cross-site GET /healthz = %d (safe methods are allowed)", rec.Code)
	}
}

func TestBuildAPIFailsOnBadKeys(t *testing.T) {
	cfg := testConfig(t)
	cfg.Encryption = config.Encryption{Keys: config.NewSecret("garbage"), ActiveKeyID: "k1"}
	if _, err := app.BuildAPI(cfg, lazyDeps(t), slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("BuildAPI accepted an unusable keyring")
	}
}
