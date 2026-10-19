// Package idstest provides deterministic ID generators for tests.
package idstest

import (
	"sync"
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/ids"
)

// Sequence returns a generator that yields want in order. Asking for more
// IDs than provided fails the test and yields uuid.Nil.
func Sequence(tb testing.TB, want ...uuid.UUID) ids.NewID {
	queue := append([]uuid.UUID(nil), want...)
	var mu sync.Mutex
	return func() uuid.UUID {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			tb.Helper()
			tb.Errorf("idstest.Sequence exhausted after %d IDs", len(want))
			return uuid.Nil()
		}
		next := queue[0]
		queue = queue[1:]
		return next
	}
}
