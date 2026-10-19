package httpx_test

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/httpx"
)

func TestCursorRoundTrip(t *testing.T) {
	id := uuid.MustParse("0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f")
	c := httpx.EncodeCursor(id)
	if strings.Contains(c, id.String()) || strings.ContainsAny(c, "+/=") {
		t.Fatalf("cursor %q is not opaque url-safe base64", c)
	}
	r := httptest.NewRequest("GET", "/v1/x?cursor="+c+"&limit=10", nil)
	p, err := httpx.ParsePage(r, 50, 100)
	if err != nil || p.After != id || p.Limit != 10 {
		t.Fatalf("ParsePage = %+v, %v", p, err)
	}
}

func TestParsePage(t *testing.T) {
	cases := []struct {
		query string
		limit int32
		err   error
	}{
		{"", 50, nil},
		{"limit=100", 100, nil},
		{"limit=1", 1, nil},
		{"limit=0", 0, httpx.ErrInvalidPage},
		{"limit=101", 0, httpx.ErrInvalidPage},
		{"limit=-5", 0, httpx.ErrInvalidPage},
		{"limit=ten", 0, httpx.ErrInvalidPage},
		{"cursor=!!!", 0, httpx.ErrInvalidPage},
		{"cursor=AAAA", 0, httpx.ErrInvalidPage},
	}
	for _, tc := range cases {
		p, err := httpx.ParsePage(httptest.NewRequest("GET", "/v1/x?"+tc.query, nil), 50, 100)
		if !errors.Is(err, tc.err) || (err == nil && (p.Limit != tc.limit || p.After != uuid.Nil())) {
			t.Errorf("%q: %+v, %v", tc.query, p, err)
		}
	}
}

func FuzzParsePage(f *testing.F) {
	f.Add("cursor=AZnD8HorfD2OT1prfI2eDw&limit=10")
	f.Add("limit=99999999999999")
	f.Fuzz(func(t *testing.T, q string) {
		r := httptest.NewRequest("GET", "/v1/x", nil)
		r.URL.RawQuery = q
		p, err := httpx.ParsePage(r, 50, 100)
		if err == nil && (p.Limit < 1 || p.Limit > 100) {
			t.Fatalf("accepted limit %d", p.Limit)
		}
	})
}

type fuzzBody struct {
	Name string `json:"name"`
}

// The shared harness itself must run cleanly over its seeds.
func FuzzHarness(f *testing.F) {
	httpxtestFuzz(f)
}
