// Package ids provides the injectable ID generator. Core code never creates
// IDs itself; it receives a NewID.
package ids

import "uuid"

// NewID returns a fresh unique identifier.
type NewID func() uuid.UUID

// System returns the production generator: time-ordered UUIDv7, which keeps
// primary-key inserts index-friendly and supports keyset pagination by ID.
func System() NewID { return uuid.NewV7 }
