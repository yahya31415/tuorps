package httpx_test

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

func TestNewServerAppliesEveryTimeoutAndLimit(t *testing.T) {
	cfg := config.HTTP{
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	h := http.NewServeMux()
	srv := httpx.NewServer(":0", h, cfg, jsonLogger(new(bytes.Buffer)))
	if srv.Addr != ":0" || srv.Handler != h {
		t.Errorf("addr/handler not set: %q %v", srv.Addr, srv.Handler)
	}
	if srv.ReadHeaderTimeout != cfg.ReadHeaderTimeout || srv.ReadTimeout != cfg.ReadTimeout ||
		srv.WriteTimeout != cfg.WriteTimeout || srv.IdleTimeout != cfg.IdleTimeout {
		t.Errorf("timeouts = %v %v %v %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
	if srv.ErrorLog == nil {
		t.Error("ErrorLog not routed to slog")
	}
}

func TestNewClientHasTimeoutsAtEveryStage(t *testing.T) {
	c := httpx.NewClient(7 * time.Second)
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T", c.Transport)
	}
	if tr.TLSHandshakeTimeout <= 0 || tr.ResponseHeaderTimeout <= 0 || tr.IdleConnTimeout <= 0 || tr.ExpectContinueTimeout <= 0 || tr.DialContext == nil {
		t.Errorf("transport timeouts not all set: %+v", tr)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS min version not 1.2")
	}
}

func TestNewClientGivesUpOnSlowServers(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	start := time.Now()
	resp, err := httpx.NewClient(100 * time.Millisecond).Get(slow.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("want timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v to time out", elapsed)
	}
}

func TestNewClientRefusesRedirectDowngradeAndLoops(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.RedirectHandler(plain.URL, http.StatusFound))
	defer secure.Close()

	c := httpx.NewClient(time.Second)
	c.Transport.(*http.Transport).TLSClientConfig = secure.Client().Transport.(*http.Transport).TLSClientConfig
	if resp, err := c.Get(secure.URL); err == nil {
		_ = resp.Body.Close()
		t.Error("followed an https -> http redirect")
	}

	// Four hops: Go's default client (10) would follow them; ours stops at 3.
	hops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := len(r.URL.Path) - len(strings.TrimRight(r.URL.Path, "x"))
		if n >= 4 {
			return
		}
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer hops.Close()
	if resp, err := http.Get(hops.URL + "/"); err != nil {
		t.Fatalf("control: default client should follow 4 hops: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	if resp, err := httpx.NewClient(time.Second).Get(hops.URL + "/"); err == nil {
		_ = resp.Body.Close()
		t.Error("followed more than 3 redirects")
	}
}

func TestRouterServesJSONForUnknownRoutesAndMethods(t *testing.T) {
	rt := httpx.NewRouter()
	rt.HandleFunc("GET /v1/posts/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
	})
	rt.Handle("POST /v1/posts", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))

	t.Run("matched", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/posts/p1", nil))
		if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"p1"`)) {
			t.Errorf("got %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("unknown path", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
		if rec.Code != http.StatusNotFound || decodeError(t, rec).Code != "not_found" {
			t.Errorf("got %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("wrong method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/posts/p1", nil))
		if rec.Code != http.StatusMethodNotAllowed || decodeError(t, rec).Code != "method_not_allowed" {
			t.Errorf("got %d %s", rec.Code, rec.Body)
		}
		if allow := rec.Header().Get("Allow"); allow == "" {
			t.Error("Allow header dropped")
		}
	})
}

func TestRouterListsRegisteredPatterns(t *testing.T) {
	rt := httpx.NewRouter()
	rt.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	rt.HandleFunc("GET /readyz", func(http.ResponseWriter, *http.Request) {})
	got := rt.Patterns()
	if !slices.Equal(got, []string{"GET /healthz", "GET /readyz"}) {
		t.Errorf("Patterns() = %v", got)
	}
	got[0] = "mutated"
	if rt.Patterns()[0] != "GET /healthz" {
		t.Error("Patterns() exposes internal state")
	}
}
