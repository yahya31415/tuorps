package core_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"testing/quick"

	"github.com/yahya31415/tuorps/internal/health/core"
)

var errDown = errors.New("connection refused: postgres://app:pw@db")

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name   string
		in     []core.CheckResult
		ready  bool
		status int
		checks []core.CheckStatus
	}{
		{"no checks", nil, true, http.StatusOK, []core.CheckStatus{}},
		{"all healthy",
			[]core.CheckResult{{Name: "postgres", Critical: true}, {Name: "redis", Critical: true}},
			true, http.StatusOK,
			[]core.CheckStatus{{Name: "postgres", Status: core.StatusOK}, {Name: "redis", Status: core.StatusOK}}},
		{"critical failure",
			[]core.CheckResult{{Name: "postgres", Critical: true}, {Name: "redis", Critical: true, Err: errDown}},
			false, http.StatusServiceUnavailable,
			[]core.CheckStatus{{Name: "postgres", Status: core.StatusOK}, {Name: "redis", Status: core.StatusFail}}},
		{"non-critical failure keeps readiness",
			[]core.CheckResult{{Name: "postgres", Critical: true}, {Name: "storage", Err: errDown}},
			true, http.StatusOK,
			[]core.CheckStatus{{Name: "postgres", Status: core.StatusOK}, {Name: "storage", Status: core.StatusFail}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := core.Evaluate(tc.in)
			if r.Ready != tc.ready || r.HTTPStatus() != tc.status {
				t.Errorf("ready=%v status=%d, want %v %d", r.Ready, r.HTTPStatus(), tc.ready, tc.status)
			}
			if !slices.Equal(r.Checks, tc.checks) {
				t.Errorf("checks = %+v, want %+v", r.Checks, tc.checks)
			}
		})
	}
}

func TestEvaluateDoesNotMutateInput(t *testing.T) {
	in := []core.CheckResult{{Name: "b", Critical: true, Err: errDown}, {Name: "a"}}
	before := slices.Clone(in)
	core.Evaluate(in)
	if !slices.Equal(in, before) {
		t.Errorf("input mutated: %+v", in)
	}
}

type flags struct{ Critical, Failing bool }

// Property: ready exactly when no critical check failed; one status per
// check, in input order.
func TestPropertyReadyIffNoCriticalFailure(t *testing.T) {
	prop := func(fs []flags) bool {
		in := make([]core.CheckResult, len(fs))
		wantReady := true
		for i, f := range fs {
			in[i] = core.CheckResult{Name: string(rune('a' + i%26)), Critical: f.Critical}
			if f.Failing {
				in[i].Err = errDown
				wantReady = wantReady && !f.Critical
			}
		}
		r := core.Evaluate(in)
		if r.Ready != wantReady || len(r.Checks) != len(in) {
			return false
		}
		for i, c := range r.Checks {
			want := core.StatusOK
			if fs[i].Failing {
				want = core.StatusFail
			}
			if c.Name != in[i].Name || c.Status != want {
				return false
			}
		}
		return (r.HTTPStatus() == http.StatusOK) == wantReady
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}
