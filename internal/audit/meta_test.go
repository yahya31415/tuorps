package audit_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/platform/clientip"
	"github.com/yahya31415/tuorps/internal/platform/httpx"
	"github.com/yahya31415/tuorps/internal/platform/ids/idstest"
)

func TestMetaFromRequest(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	resolver, err := clientip.New([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	var got core.Meta
	reqID := uuid.MustParse("0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f")
	h := httpx.RequestID(idstest.Sequence(t, reqID))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = audit.MetaFromRequest(r, resolver, key)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.2:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.RequestID != reqID.String() {
		t.Errorf("RequestID = %q", got.RequestID)
	}
	if got.IPHash != core.HashIP(key, netip.MustParseAddr("203.0.113.7")) {
		t.Errorf("IPHash = %q, want the hash of the forwarded client address", got.IPHash)
	}
}
