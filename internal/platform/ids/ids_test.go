package ids_test

import (
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/ids/idstest"
)

func TestSystemGeneratesDistinctV7IDs(t *testing.T) {
	next := ids.System()
	a, b := next(), next()
	if a == b {
		t.Fatalf("two calls returned the same ID %v", a)
	}
	for _, id := range []uuid.UUID{a, b} {
		if v := id[6] >> 4; v != 7 {
			t.Errorf("%v has version %d, want 7", id, v)
		}
	}
	if a.String() >= b.String() {
		t.Errorf("v7 IDs not time-ordered: %v then %v", a, b)
	}
}

func TestSequenceReplaysIDsInOrder(t *testing.T) {
	x, y := uuid.MustParse("0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e01"), uuid.MustParse("0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e02")
	next := idstest.Sequence(t, x, y)
	if got := next(); got != x {
		t.Fatalf("first = %v, want %v", got, x)
	}
	if got := next(); got != y {
		t.Fatalf("second = %v, want %v", got, y)
	}
}

func TestSequenceFailsTheTestWhenExhausted(t *testing.T) {
	ft := &fakeTB{TB: t}
	next := idstest.Sequence(ft)
	if got := next(); got != uuid.Nil() || !ft.failed {
		t.Fatalf("exhausted Sequence returned %v, failed=%v; want Nil and a test failure", got, ft.failed)
	}
}

type fakeTB struct {
	testing.TB
	failed bool
}

func (f *fakeTB) Helper()               {}
func (f *fakeTB) Errorf(string, ...any) { f.failed = true }
