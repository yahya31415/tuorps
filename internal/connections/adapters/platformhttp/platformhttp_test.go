package platformhttp_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/adapters/platformhttp"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

const secret = "SECRET-app-secret-value"

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func get(t *testing.T, base string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/oauth/access_token?client_secret="+secret+"&code="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Errors carry the provider, the operation, the status and a fixed code,
// and never the URL (which can hold a client secret) or the body (which
// can echo tokens or addresses) (ADR 0009).
func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	if msg := err.Error(); strings.Contains(msg, secret) || strings.Contains(msg, "access_token") || strings.Contains(msg, "127.0.0.1") {
		t.Errorf("error text leaks request details: %s", msg)
	}
	var urlErr interface{ Timeout() bool }
	if errors.As(err, &urlErr) {
		t.Errorf("a *url.Error (with its URL) is still in the chain: %v", err)
	}
}

func TestDo(t *testing.T) {
	c := platformhttp.New(core.ProviderMeta, 2*time.Second, 1<<10)

	t.Run("returns the body of a success", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) })
		body, err := c.Do(get(t, s.URL), "exchange")
		if err != nil || string(body) != `{"ok":true}` {
			t.Fatalf("Do = %q, %v", body, err)
		}
	})

	t.Run("platform errors are classified and scrubbed", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"bad token `+secret+`","type":"OAuthException","code":190}}`)
		})
		_, err := c.Do(get(t, s.URL), "exchange")
		assertClean(t, err)
		if !errors.Is(err, core.ErrReauthRequired) {
			t.Errorf("err = %v, want reauth_required", err)
		}
		var pe *platformhttp.Error
		if !errors.As(err, &pe) || pe.Provider != core.ProviderMeta || pe.Op != "exchange" || pe.Status != 400 {
			t.Errorf("err = %#v", err)
		}
	})

	t.Run("redirects are not followed", func(t *testing.T) {
		var followed bool
		target := server(t, func(http.ResponseWriter, *http.Request) { followed = true })
		s := server(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
		_, err := c.Do(get(t, s.URL), "exchange")
		assertClean(t, err)
		if followed || !errors.Is(err, core.ErrRejected) {
			t.Errorf("followed = %v, err = %v", followed, err)
		}
	})

	t.Run("oversized bodies are refused", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 2<<10)) })
		_, err := c.Do(get(t, s.URL), "exchange")
		assertClean(t, err)
		if !errors.Is(err, core.ErrRejected) {
			t.Errorf("err = %v, want rejected", err)
		}
	})

	t.Run("transport failures are unavailable, without the URL", func(t *testing.T) {
		s := server(t, func(http.ResponseWriter, *http.Request) {})
		s.Close()
		_, err := c.Do(get(t, s.URL), "exchange")
		assertClean(t, err)
		if !errors.Is(err, core.ErrProviderUnavailable) {
			t.Errorf("err = %v, want provider_unavailable", err)
		}
	})

	t.Run("timeouts are unavailable", func(t *testing.T) {
		slow := platformhttp.New(core.ProviderGoogle, 50*time.Millisecond, 1<<10)
		s := server(t, func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) })
		_, err := slow.Do(get(t, s.URL), "refresh")
		assertClean(t, err)
		if !errors.Is(err, core.ErrProviderUnavailable) {
			t.Errorf("err = %v, want provider_unavailable", err)
		}
	})
}
