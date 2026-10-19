package scripts_test

import (
	"fmt"
	"strings"
	"testing"
)

const mod = "github.com/yahya31415/tuorps"

// profile renders a cover profile with n blocks of one statement each for
// file, of which covered are hit.
func profile(blocks ...string) string {
	return "mode: atomic\n" + strings.Join(blocks, "")
}

func blocks(file string, n, covered int) string {
	var b strings.Builder
	for i := range n {
		hits := 0
		if i < covered {
			hits = 1
		}
		fmt.Fprintf(&b, "%s/%s:%d.1,%d.2 1 %d\n", mod, file, i+1, i+1, hits)
	}
	return b.String()
}

func gate(t *testing.T, prof string, args ...string) result {
	t.Helper()
	dir := t.TempDir()
	p := writeFile(t, dir, "coverage.out", prof)
	return run(t, dir, nil, "", scriptPath(t, "coverage-gate.sh"), append([]string{p}, args...)...)
}

func TestCoverageGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		profile  string
		wantCode int
		want     string
	}{
		{"both thresholds met",
			profile(blocks("internal/a/core/x.go", 10, 9), blocks("internal/a/http/h.go", 10, 7)),
			0, "core: 90.0%"},
		{"core below 90",
			profile(blocks("internal/a/core/x.go", 10, 8), blocks("internal/a/http/h.go", 10, 10)),
			1, "core: 80.0%"},
		{"overall below 80",
			profile(blocks("internal/a/core/x.go", 10, 10), blocks("internal/a/http/h.go", 30, 20)),
			1, "overall: 75.0%"},
		{"generated sqlc code excluded",
			profile(blocks("internal/a/core/x.go", 10, 10), blocks("internal/a/adapters/postgres/sqlcgen/q.go", 100, 0)),
			0, "overall: 100.0%"},
		{"no core packages yet",
			profile(blocks("internal/platform/config/c.go", 10, 9)),
			0, "core: n/a"},
		{"duplicate blocks counted once, covered if hit anywhere",
			profile(blocks("internal/a/core/x.go", 10, 0), blocks("internal/a/core/x.go", 10, 10)),
			0, "core: 100.0%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := gate(t, tc.profile)
			if r.code != tc.wantCode {
				t.Fatalf("exit = %d, want %d\n%s", r.code, tc.wantCode, r.output())
			}
			if !strings.Contains(r.output(), tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, r.output())
			}
		})
	}
}

func TestCoverageGateCustomThresholds(t *testing.T) {
	t.Parallel()
	prof := profile(blocks("internal/a/core/x.go", 10, 5))
	if r := gate(t, prof, "50", "50"); r.code != 0 {
		t.Fatalf("exit = %d, want 0 with 50/50 thresholds\n%s", r.code, r.output())
	}
}

func TestCoverageGateMissingProfile(t *testing.T) {
	t.Parallel()
	r := run(t, t.TempDir(), nil, "", scriptPath(t, "coverage-gate.sh"), "/nonexistent/coverage.out")
	if r.code == 0 {
		t.Fatal("exit = 0 for a missing profile")
	}
}
