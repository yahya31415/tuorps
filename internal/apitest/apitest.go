// Package apitest runs the complete API (app.BuildAPI) over real Postgres,
// River and Redis for end-to-end handler tests, with browser-like clients
// and access to queued email. Import it only from _test.go files.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	connscore "github.com/yahya31415/tuorps/internal/connections/core"
	connhttp "github.com/yahya31415/tuorps/internal/connections/http"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/app"
	"github.com/yahya31415/tuorps/internal/email"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// Password is the password every helper-created account uses.
const Password = "correct horse battery staple"

const testKeys = "k1:BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="

// API is a running API.
type API struct {
	tb     testing.TB
	Server *httptest.Server
	Admin  *pgxpool.Pool // superuser, for assertions
	keys   *keyring.Keyring
	nextIP atomic.Uint32
}

type settings struct {
	platforms map[connscore.Provider]connhttp.Platform
	env       map[string]string
}

// Option adjusts the API Start builds.
type Option func(*settings)

// WithPlatforms replaces the platform integrations (tests use fakes).
func WithPlatforms(p map[connscore.Provider]connhttp.Platform) Option {
	return func(s *settings) { s.platforms = p }
}

// WithEnv adds configuration variables (for example platform credentials).
func WithEnv(kv map[string]string) Option {
	return func(s *settings) { s.env = kv }
}

// Start runs the API and stops it when tb ends.
func Start(tb testing.TB, opts ...Option) *API {
	tb.Helper()
	var set settings
	for _, o := range opts {
		o(&set)
	}
	pg := testinfra.MigratedPostgres(tb)
	rd := testinfra.StartRedis(tb)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	migrator, err := postgres.NewPool(ctx, config.DB{URL: config.NewSecret(pg.MigratorURL), MaxConns: 2, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute})
	if err != nil {
		tb.Fatal(err)
	}
	if err := jobs.Migrate(ctx, migrator, log); err != nil {
		tb.Fatal(err)
	}
	migrator.Close()

	env := map[string]string{
		"DATABASE_URL":             pg.AppURL,
		"REDIS_URL":                rd.URL,
		"ENCRYPTION_KEYS":          testKeys,
		"ENCRYPTION_ACTIVE_KEY_ID": "k1",
		"APP_ORIGIN":               "https://app.tuorps.test",
		"TRUSTED_PROXY_CIDRS":      "127.0.0.0/8", // clients pick their address via X-Forwarded-For
	}
	for k, v := range set.env {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	if err != nil {
		tb.Fatal(err)
	}
	deps, err := app.Open(ctx, cfg)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = deps.Close() })
	deps.Platforms = set.platforms
	built, err := app.BuildAPI(cfg, deps, log)
	if err != nil {
		tb.Fatal(err)
	}
	srv := httptest.NewTLSServer(built.Handler)
	tb.Cleanup(srv.Close)
	admin, err := postgres.NewPool(ctx, config.DB{URL: config.NewSecret(pg.AdminURL), MaxConns: 4, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(admin.Close)
	keys, err := cfg.Encryption.Keyring()
	if err != nil {
		tb.Fatal(err)
	}
	return &API{tb: tb, Server: srv, Admin: admin, keys: keys}
}

// Client is a browser-like client with its own cookie jar and client IP.
type Client struct {
	api  *API
	http *http.Client
	ip   string
}

// Client returns a new client with a fresh address.
func (a *API) Client() *Client {
	n := a.nextIP.Add(1)
	jar, _ := cookiejar.New(nil)
	return &Client{
		api: a,
		http: &http.Client{Transport: a.Server.Client().Transport, Jar: jar, Timeout: 30 * time.Second,
			// The API's redirects (the OAuth callback) point at the browser
			// app; tests inspect them instead of following them.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		ip: fmt.Sprintf("10.%d.%d.%d", n>>16&255, n>>8&255, n&255),
	}
}

// Response is a completed request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v.
func (r Response) JSON(tb testing.TB, v any) {
	tb.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		tb.Fatalf("body %s: %v", r.Body, err)
	}
}

// ErrorCode is the canonical error code of the body.
func (r Response) ErrorCode(tb testing.TB) string {
	tb.Helper()
	var e httpx.ErrorBody
	r.JSON(tb, &e)
	return e.Error.Code
}

// Do sends a JSON request (body may be nil).
func (c *Client) Do(method, path string, body any) Response {
	c.api.tb.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.api.tb.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.api.Server.URL+path, rd)
	if err != nil {
		c.api.tb.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forwarded-For", c.ip)
	resp, err := c.http.Do(req)
	if err != nil {
		c.api.tb.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

func (c *Client) Get(path string) Response              { return c.Do(http.MethodGet, path, nil) }
func (c *Client) Post(path string, body any) Response   { return c.Do(http.MethodPost, path, body) }
func (c *Client) Patch(path string, body any) Response  { return c.Do(http.MethodPatch, path, body) }
func (c *Client) Put(path string, body any) Response    { return c.Do(http.MethodPut, path, body) }
func (c *Client) Delete(path string, body any) Response { return c.Do(http.MethodDelete, path, body) }

// Emails decrypts every queued email, oldest first.
func (a *API) Emails() []emailcore.Message {
	a.tb.Helper()
	rows, err := a.Admin.Query(context.Background(), `SELECT args FROM tuorps.river_job WHERE kind = 'email.send' ORDER BY id`)
	if err != nil {
		a.tb.Fatal(err)
	}
	defer rows.Close()
	var out []emailcore.Message
	for rows.Next() {
		var args email.SendArgs
		if err := rows.Scan(&args); err != nil {
			a.tb.Fatal(err)
		}
		s, err := keyring.Decode(args.Sealed)
		if err != nil {
			a.tb.Fatal(err)
		}
		plain, err := a.keys.Open(s, []byte(email.SendArgsAAD))
		if err != nil {
			a.tb.Fatal(err)
		}
		m, err := emailcore.ParseMessage(plain)
		if err != nil {
			a.tb.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// LastEmailTo is the newest email of kind to addr (case-insensitive).
func (a *API) LastEmailTo(addr string, kind emailcore.Kind) (emailcore.Message, bool) {
	a.tb.Helper()
	for _, m := range slices.Backward(a.Emails()) {
		if strings.EqualFold(m.To().Address, addr) && m.Kind() == kind {
			return m, true
		}
	}
	return emailcore.Message{}, false
}

// TokenFrom extracts the token from an email link's fragment.
func TokenFrom(tb testing.TB, m emailcore.Message) string {
	tb.Helper()
	u, err := url.Parse(m.Data()["link"])
	if err != nil {
		tb.Fatal(err)
	}
	v, _ := url.ParseQuery(u.Fragment)
	return v.Get("token")
}

// SignUp creates and verifies an account (with its own workspace) and
// returns a logged-in client.
func (a *API) SignUp(emailAddr, workspaceName string) *Client {
	a.tb.Helper()
	c := a.Client()
	if r := c.Post("/v1/auth/signup", map[string]string{"email": emailAddr, "password": Password, "name": "User " + emailAddr, "workspace_name": workspaceName}); r.Status != http.StatusAccepted {
		a.tb.Fatalf("signup %s: %d %s", emailAddr, r.Status, r.Body)
	}
	m, ok := a.LastEmailTo(emailAddr, emailcore.KindVerifyEmail)
	if !ok {
		a.tb.Fatalf("no verification email for %s", emailAddr)
	}
	if r := c.Post("/v1/auth/verify-email", map[string]string{"token": TokenFrom(a.tb, m)}); r.Status != http.StatusNoContent {
		a.tb.Fatalf("verify %s: %d %s", emailAddr, r.Status, r.Body)
	}
	if r := c.Post("/v1/auth/login", map[string]string{"email": emailAddr, "password": Password}); r.Status != http.StatusOK {
		a.tb.Fatalf("login %s: %d %s", emailAddr, r.Status, r.Body)
	}
	return c
}

// Count runs a counting query as the superuser.
func (a *API) Count(q string, args ...any) int {
	a.tb.Helper()
	var n int
	if err := a.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		a.tb.Fatal(err)
	}
	return n
}
