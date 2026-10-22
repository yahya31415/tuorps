package connections

import "testing"

// connections.warned_days only accepts 0, 3 or 14 (migration 00003); the
// column value is derived so it can never overflow or violate the check.
func TestWarnedDaysColumn(t *testing.T) {
	for in, want := range map[int]int16{0: 0, 3: 3, 14: 14, 7: 0, -1: 0, 1 << 20: 0} {
		if got := warnedDaysColumn(in); got != want {
			t.Errorf("warnedDaysColumn(%d) = %d, want %d", in, got, want)
		}
	}
}
