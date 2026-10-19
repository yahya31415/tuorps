package httpx

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"uuid"
)

// ErrInvalidPage reports a malformed cursor or an out-of-range limit.
var ErrInvalidPage = errors.New("invalid cursor or limit")

// Page is a keyset page request: rows after (or before) After, at most Limit.
type Page struct {
	After uuid.UUID // uuid.Nil for the first page
	Limit int32
}

// EncodeCursor renders a row ID as an opaque cursor.
func EncodeCursor(id uuid.UUID) string { return base64.RawURLEncoding.EncodeToString(id[:]) }

// ParsePage reads ?cursor= and ?limit= (default def, at most max).
func ParsePage(r *http.Request, def, max int32) (Page, error) {
	p := Page{Limit: def}
	q := r.URL.Query()
	if s := q.Get("limit"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil || n < 1 || n > int64(max) {
			return Page{}, ErrInvalidPage
		}
		p.Limit = int32(n)
	}
	if s := q.Get("cursor"); s != "" {
		b, err := base64.RawURLEncoding.Strict().DecodeString(s)
		if err != nil || len(b) != len(p.After) {
			return Page{}, ErrInvalidPage
		}
		copy(p.After[:], b)
	}
	return p, nil
}
