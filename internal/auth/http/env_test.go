package authhttp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/auth"
	"github.com/yahya31415/tuorps/internal/auth/core"
	authhttp "github.com/yahya31415/tuorps/internal/auth/http"
	"github.com/yahya31415/tuorps/internal/email"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/ratelimit"
	"github.com/yahya31415/tuorps/internal/platform/redisx"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
	"github.com/yahya31415/tuorps/internal/workspace"
)

// env is one running auth API over real Postgres, River and Redis.
type env struct {
	t      *testing.T
	srv    *httptest.Server
	admin  *pgxpool.Pool
	rdb    *redis.Client
	keys   *keyring.Keyring
	origin *url.URL
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pg := testinfra.MigratedPostgres(t)
	rd := testinfra.StartRedis(t)
	mk := func(u string) *pgxpool.Pool {
		p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(u), MaxConns: 8, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	migrator, app, admin := mk(pg.MigratorURL), mk(pg.AppURL), mk(pg.AdminURL)
	log := slog.New(slog.DiscardHandler)
	if err := jobs.Migrate(t.Context(), migrator, log); err != nil {
		t.Fatal(err)
	}
	rdb, err := redisx.NewClient(t.Context(), config.Redis{URL: config.NewSecret(rd.URL), PoolSize: 8, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	keys, err := keyring.New(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := jobs.NewInserter(app, log)
	if err != nil {
		t.Fatal(err)
	}
	origin := &url.URL{Scheme: "https", Host: "app.tuorps.test"}
	resolver, _ := clientip.New([]string{"127.0.0.0/8"})
	policy := core.SessionPolicy{Idle: 12 * time.Hour, Absolute: 14 * 24 * time.Hour}
	d := authhttp.Deps{
		Pool:        app,
		Writer:      audit.NewWriter(app, ids.System(), clock.System()),
		Hasher:      auth.NewHasher(core.Argon2Params{Memory: 64, Time: 1, Threads: 1}, 4, rand.Reader),
		Sessions:    auth.NewSessionStore(rdb, policy, clock.System(), rand.Reader),
		Challenges:  auth.NewChallengeStore(rdb, 5*time.Minute, rand.Reader),
		Mailer:      email.NewOutbox(inserter, keys, rand.Reader),
		Keys:        keys,
		Limiter:     ratelimit.NewLimiter(ratelimit.RedisStore(rdb), clock.System(), log),
		ClientIP:    resolver,
		Origin:      origin,
		SessionLife: policy.Absolute,
		Now:         clock.System(),
		NewID:       ids.System(),
		Rand:        rand.Reader,
		Log:         log,
		Bootstrap:   workspace.Bootstrap,
	}
	rt := httpx.NewRouter()
	if err := authhttp.Register(rt, d); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(httpx.Chain(rt, httpx.RequestID(ids.System()))) // Secure cookies need HTTPS
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, admin: admin, rdb: rdb, keys: keys, origin: origin}
}

// client is a browser-like client with its own cookie jar and client IP.
type client struct {
	e    *env
	http *http.Client
	ip   string
}

func (e *env) client(ip string) *client {
	jar, _ := cookiejar.New(nil)
	// srv.Client() is shared; give each browser its own client and jar.
	hc := &http.Client{Transport: e.srv.Client().Transport, Jar: jar, Timeout: 30 * time.Second}
	return &client{e: e, http: hc, ip: ip}
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("body %s: %v", r.Body, err)
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var e httpx.ErrorBody
	r.json(t, &e)
	return e.Error.Code
}

func (c *client) do(method, path string, body any) response {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forwarded-For", c.ip)
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

func (c *client) post(path string, body any) response { return c.do(http.MethodPost, path, body) }
func (c *client) get(path string) response            { return c.do(http.MethodGet, path, nil) }

// sentEmails decrypts every queued email, oldest first.
func (e *env) sentEmails() []emailcore.Message {
	e.t.Helper()
	rows, err := e.admin.Query(context.Background(), `SELECT args FROM tuorps.river_job WHERE kind = 'email.send' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []emailcore.Message
	for rows.Next() {
		var args email.SendArgs
		if err := rows.Scan(&args); err != nil {
			e.t.Fatal(err)
		}
		s, err := keyring.Decode(args.Sealed)
		if err != nil {
			e.t.Fatal(err)
		}
		plain, err := e.keys.Open(s, []byte(email.SendArgsAAD))
		if err != nil {
			e.t.Fatal(err)
		}
		m, err := emailcore.ParseMessage(plain)
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// lastEmailTo returns the newest email to addr of kind, failing if none.
func (e *env) lastEmailTo(addr string, kind emailcore.Kind) emailcore.Message {
	e.t.Helper()
	all := e.sentEmails()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].To().Address == addr && all[i].Kind() == kind {
			return all[i]
		}
	}
	e.t.Fatalf("no %s email to %s", kind, addr)
	return emailcore.Message{}
}

func (e *env) countEmails(addr string, kind emailcore.Kind) int {
	n := 0
	for _, m := range e.sentEmails() {
		if m.To().Address == addr && m.Kind() == kind {
			n++
		}
	}
	return n
}

// tokenFrom extracts the token from an email link's fragment.
func tokenFrom(t *testing.T, m emailcore.Message) string {
	t.Helper()
	u, err := url.Parse(m.Data()["link"])
	if err != nil {
		t.Fatal(err)
	}
	v, _ := url.ParseQuery(u.Fragment)
	if v.Get("token") == "" {
		t.Fatalf("link %q has no token", m.Data()["link"])
	}
	return v.Get("token")
}

// signupAndVerify creates a verified account and returns a logged-in client.
func (e *env) signupAndVerify(emailAddr, ip string) *client {
	e.t.Helper()
	c := e.client(ip)
	if r := c.post("/v1/auth/signup", map[string]string{"email": emailAddr, "password": "correct horse battery staple", "name": "Priya", "workspace_name": "Kerala Spices"}); r.Status != http.StatusAccepted {
		e.t.Fatalf("signup %s: %d %s", emailAddr, r.Status, r.Body)
	}
	tok := tokenFrom(e.t, e.lastEmailTo(strings.ToLower(emailAddr), emailcore.KindVerifyEmail))
	if r := c.post("/v1/auth/verify-email", map[string]string{"token": tok}); r.Status != http.StatusNoContent {
		e.t.Fatalf("verify: %d %s", r.Status, r.Body)
	}
	if r := c.post("/v1/auth/login", map[string]string{"email": emailAddr, "password": "correct horse battery staple"}); r.Status != http.StatusOK {
		e.t.Fatalf("login: %d %s", r.Status, r.Body)
	}
	return c
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}
