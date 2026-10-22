// Package platformhttp is the HTTP client every platform adapter uses: the
// hardened httpx client with redirects disabled, response bodies capped,
// and failures reduced to a provider, an operation, a status and a fixed
// core.ErrorCode. Neither the URL (Meta and Instagram put the app secret in
// query strings) nor the response body ever reaches an error (ADR 0009).
package platformhttp

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

// Error is a failed platform call. It unwraps to its Code.
type Error struct {
	Provider core.Provider
	Op       string
	Status   int // 0: no response
	Code     core.ErrorCode
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: status %d: %s", e.Provider, e.Op, e.Status, e.Code)
}

func (e *Error) Unwrap() error { return e.Code }

// Client calls one provider.
type Client struct {
	http     *http.Client
	provider core.Provider
	maxBody  int64
}

// New returns a Client for p with an overall timeout per call and a cap on
// response bodies.
func New(p core.Provider, timeout time.Duration, maxBody int64) Client {
	c := *httpx.NewClient(timeout)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Client{http: &c, provider: p, maxBody: maxBody}
}

// Do sends req and returns the body of a 2xx response. op names the call in
// errors ("exchange", "refresh", ...).
func (c Client) Do(req *http.Request, op string) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport error embeds the URL; only its class survives.
		return nil, &Error{Provider: c.provider, Op: op, Code: core.ErrProviderUnavailable}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, &Error{Provider: c.provider, Op: op, Status: resp.StatusCode, Code: core.ErrProviderUnavailable}
	}
	if int64(len(body)) > c.maxBody {
		return nil, &Error{Provider: c.provider, Op: op, Status: resp.StatusCode, Code: core.ErrRejected}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Error{Provider: c.provider, Op: op, Status: resp.StatusCode, Code: core.ClassifyProviderError(c.provider, resp.StatusCode, body)}
	}
	return body, nil
}
