package health

import (
	"context"
	"sync"
	"time"

	"github.com/yahya31415/tuorps/internal/health/core"
)

// RunChecks runs every check concurrently, each bounded by timeout, and
// returns their results in input order. A check that ignores its context is
// abandoned at the deadline rather than waited for.
func RunChecks(ctx context.Context, timeout time.Duration, checks []NamedCheck) []core.CheckResult {
	results := make([]core.CheckResult, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Run(cctx) }()
			var err error
			select {
			case err = <-done:
			case <-cctx.Done():
				err = cctx.Err()
			}
			results[i] = core.CheckResult{Name: c.Name, Critical: c.Critical, Err: err}
		})
	}
	wg.Wait()
	return results
}
