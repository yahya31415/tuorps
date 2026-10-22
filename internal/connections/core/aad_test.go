package core_test

import (
	"testing"
	"uuid"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Each ciphertext is bound to its row and field, so it cannot be moved to
// another row or swapped between access, refresh and Page tokens (ADR 0009).
func TestTokenAAD(t *testing.T) {
	a := uuid.MustParse("0199c3f0-0000-7000-8000-000000000001")
	b := uuid.MustParse("0199c3f0-0000-7000-8000-000000000002")
	if got := string(core.TokenAAD(a, core.FieldAccessToken)); got != "connections/0199c3f0-0000-7000-8000-000000000001/access_token" {
		t.Errorf("AAD = %s", got)
	}
	seen := map[string]bool{}
	for _, id := range []uuid.UUID{a, b} {
		for _, f := range []core.TokenField{core.FieldAccessToken, core.FieldRefreshToken, core.FieldPageToken} {
			k := string(core.TokenAAD(id, f))
			if seen[k] {
				t.Errorf("AAD %s repeats", k)
			}
			seen[k] = true
		}
	}
}
