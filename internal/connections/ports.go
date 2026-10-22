package connections

import (
	"context"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Provider is what the connection flow and maintenance need from one
// platform. Adapters in connections/adapters implement it; tests substitute
// fakes. A nil function means the platform has no such operation.
type Provider struct {
	// Exchange turns an authorization code into the grant's stored token
	// (long-lived where the platform has one), with the platform user id.
	Exchange func(ctx context.Context, code string) (core.TokenSet, error)
	// Refresh renews a token: nil where tokens are not refreshed.
	Refresh func(ctx context.Context, token string) (core.TokenSet, error)
	// ListAccounts lists the accounts the grant's token can connect.
	ListAccounts func(ctx context.Context, token string) ([]core.Account, error)
	// Check reports whether a token still works.
	Check func(ctx context.Context, token string) (core.Health, error)
	// Revoke withdraws the grant at the platform: nil where none is documented.
	Revoke func(ctx context.Context, token string) error
}
