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

// ListResponse is a keyset page: items, and next_cursor when more may follow.
type ListResponse[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// NewList builds a page from items fetched with limit. Items is never nil,
// and NextCursor points past the last item only when the page is full.
func NewList[T any](items []T, limit int32, lastID func(T) uuid.UUID) ListResponse[T] {
	out := ListResponse[T]{Items: items}
	if out.Items == nil {
		out.Items = []T{}
	}
	if len(items) > 0 && len(items) == int(limit) {
		c := EncodeCursor(lastID(items[len(items)-1]))
		out.NextCursor = &c
	}
	return out
}
