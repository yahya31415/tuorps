// Package clock provides the injectable time source used across the shell.
// Core code never calls time.Now; it receives a Now.
package clock

import "time"

// Now returns the current instant.
type Now func() time.Time

// System returns the real clock, in UTC.
func System() Now { return func() time.Time { return time.Now().UTC() } }

// Fixed returns a clock frozen at t, for tests and deterministic replays.
func Fixed(t time.Time) Now { return func() time.Time { return t } }
