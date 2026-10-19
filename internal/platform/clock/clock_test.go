package clock_test

import (
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/platform/clock"
)

func TestSystemReturnsCurrentUTCTime(t *testing.T) {
	before := time.Now()
	got := clock.System()()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System()() = %v, want within [%v, %v]", got, before, after)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
}

func TestFixedAlwaysReturnsTheSameInstant(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	now := clock.Fixed(at)
	if !now().Equal(at) || !now().Equal(at) {
		t.Fatalf("Fixed(%v)() = %v", at, now())
	}
}
