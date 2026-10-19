// Package core holds the pure readiness rules: given the outcome of each
// dependency check, decide whether the process can serve traffic.
package core

import "net/http"

// Status is the public outcome of one check. It deliberately carries no
// error detail; details are for logs, never for responses.
type Status string

const (
	StatusOK   Status = "ok"
	StatusFail Status = "fail"
)

// CheckResult is the outcome of running one dependency check.
type CheckResult struct {
	Name     string
	Critical bool // a failing critical check makes the process not ready
	Err      error
}

// CheckStatus is the public view of one check.
type CheckStatus struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// Report is the readiness verdict.
type Report struct {
	Ready  bool          `json:"ready"`
	Checks []CheckStatus `json:"checks"`
}

// Evaluate derives a Report from results, preserving their order. The
// process is ready iff no critical check failed.
func Evaluate(results []CheckResult) Report {
	r := Report{Ready: true, Checks: make([]CheckStatus, len(results))}
	for i, res := range results {
		st := StatusOK
		if res.Err != nil {
			st = StatusFail
			r.Ready = r.Ready && !res.Critical
		}
		r.Checks[i] = CheckStatus{Name: res.Name, Status: st}
	}
	return r
}

// HTTPStatus maps the verdict to 200 or 503.
func (r Report) HTTPStatus() int {
	if r.Ready {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}
