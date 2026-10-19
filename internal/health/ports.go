// Package health wires dependency checks to the pure readiness rules in
// health/core. Checks are side-effecting functions supplied by adapters.
package health

import "context"

// Check probes one dependency; it must honour ctx cancellation.
type Check func(ctx context.Context) error

// NamedCheck is a Check with its public name and criticality.
type NamedCheck struct {
	Name     string
	Critical bool
	Run      Check
}
