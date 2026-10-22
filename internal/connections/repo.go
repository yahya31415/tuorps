// Package connections is the shell for platform connections: the
// sqlc-backed repository, the token vault and the OAuth state store. Rules
// live in connections/core (ADRs 0008-0010).
package connections

import (
	"context"
	"errors"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/connections/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/connections/core"
)

// Repository errors. ErrNotFound also covers rows hidden by row-level
// security, so another workspace's data is indistinguishable from absent data.
var (
	ErrNotFound         = errors.New("connections: not found")
	ErrAlreadyConnected = errors.New("connections: this account is already connected in this workspace")
)

// NewGrant is a completed OAuth flow to store. Tokens are keyring
// ciphertexts; empty strings and zero times are stored as NULL.
type NewGrant struct {
	ID, BrandID           uuid.UUID
	Provider              core.Provider
	ProviderUserID        string
	Scopes, MissingScopes []string
	AccessTokenEnc        string
	RefreshTokenEnc       string
	AccessExpiresAt       time.Time
	RefreshExpiresAt      time.Time
	DataAccessExpiresAt   time.Time
	TokenIssuedAt         time.Time
	CreatedBy             uuid.UUID
	Now                   time.Time
}

// Grant is a stored OAuth grant; NULLs read as empty strings and zero times.
type Grant struct {
	ID, BrandID           uuid.UUID
	Provider              core.Provider
	ProviderUserID        string
	Scopes, MissingScopes []string
	AccessTokenEnc        string
	RefreshTokenEnc       string
	AccessExpiresAt       time.Time
	RefreshExpiresAt      time.Time
	DataAccessExpiresAt   time.Time
	TokenIssuedAt         time.Time
	CreatedBy             uuid.UUID
	CreatedAt             time.Time
}

// NewConnection is an account to attach to a brand; it starts active.
type NewConnection struct {
	ID, BrandID, GrantID uuid.UUID
	Kind                 core.Kind
	ExternalID           string
	DisplayName          string
	TokenEnc             string // Facebook Page token, or empty
	TokenExpiresAt       time.Time
	NextDueAt            time.Time
	Now                  time.Time
}

// Connection is a stored connection; NULLs read as empty strings and zero
// times. Provider is its grant's.
type Connection struct {
	ID, BrandID, GrantID uuid.UUID
	Provider             core.Provider
	Kind                 core.Kind
	ExternalID           string
	DisplayName          string
	TokenEnc             string
	TokenKeyID           string
	Status               core.Status
	TokenExpiresAt       time.Time
	LastCheckedAt        time.Time
	LastError            core.ErrorCode
	WarnedDays           int
	NextDueAt            time.Time
	CreatedAt            time.Time
}

func q(tx pgx.Tx) *sqlcgen.Queries { return sqlcgen.New(tx) }

// InsertGrant stores a grant in the transaction's workspace.
func InsertGrant(ctx context.Context, tx audit.Tx, g NewGrant) error {
	return mapErr(q(tx.Pgx()).InsertGrant(ctx, sqlcgen.InsertGrantParams{
		ID: g.ID, WorkspaceID: tx.Scope().WorkspaceID, BrandID: g.BrandID,
		Provider: string(g.Provider), ProviderUserID: g.ProviderUserID,
		Scopes: nonNil(g.Scopes), MissingScopes: nonNil(g.MissingScopes),
		AccessTokenEnc: nullString(g.AccessTokenEnc), RefreshTokenEnc: nullString(g.RefreshTokenEnc),
		AccessExpiresAt: nullTime(g.AccessExpiresAt), RefreshExpiresAt: nullTime(g.RefreshExpiresAt),
		DataAccessExpiresAt: nullTime(g.DataAccessExpiresAt), TokenIssuedAt: nullTime(g.TokenIssuedAt),
		CreatedBy: g.CreatedBy, Now: g.Now,
	}))
}

// GetGrant reads one grant of a brand.
func GetGrant(ctx context.Context, tx pgx.Tx, ws, brand, id uuid.UUID) (Grant, error) {
	r, err := q(tx).GetGrant(ctx, sqlcgen.GetGrantParams{WorkspaceID: ws, BrandID: brand, ID: id})
	if err != nil {
		return Grant{}, mapErr(err)
	}
	return grantFromRow(r), nil
}

// WipeGrantTokens deletes a grant's tokens, keeping the row for audit.
func WipeGrantTokens(ctx context.Context, tx audit.Tx, brand, id uuid.UUID, now time.Time) error {
	return affected(q(tx.Pgx()).WipeGrantTokens(ctx, sqlcgen.WipeGrantTokensParams{WorkspaceID: tx.Scope().WorkspaceID, BrandID: brand, ID: id, Now: now}))
}

// InsertConnection attaches an account to a brand. ErrAlreadyConnected: the
// account is live in this workspace already; ErrNotFound: the grant is not
// this brand's.
func InsertConnection(ctx context.Context, tx audit.Tx, c NewConnection) error {
	return mapErr(q(tx.Pgx()).InsertConnection(ctx, sqlcgen.InsertConnectionParams{
		ID: c.ID, WorkspaceID: tx.Scope().WorkspaceID, BrandID: c.BrandID, GrantID: c.GrantID,
		Kind: string(c.Kind), ExternalID: c.ExternalID, DisplayName: c.DisplayName,
		TokenEnc: nullString(c.TokenEnc), TokenExpiresAt: nullTime(c.TokenExpiresAt), NextDueAt: nullTime(c.NextDueAt),
		Now: c.Now,
	}))
}

// GetConnection reads one connection of a brand.
func GetConnection(ctx context.Context, tx pgx.Tx, ws, brand, id uuid.UUID) (Connection, error) {
	r, err := q(tx).GetConnection(ctx, sqlcgen.GetConnectionParams{WorkspaceID: ws, BrandID: brand, ID: id})
	if err != nil {
		return Connection{}, mapErr(err)
	}
	return connection(r), nil
}

// ListConnections pages a brand's connections by id (keyset).
func ListConnections(ctx context.Context, tx pgx.Tx, ws, brand, after uuid.UUID, limit int32) ([]Connection, error) {
	rows, err := q(tx).ListConnections(ctx, sqlcgen.ListConnectionsParams{WorkspaceID: ws, BrandID: brand, After: after, PageSize: limit})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, connection(sqlcgen.GetConnectionRow(r)))
	}
	return out, nil
}

// Disconnect marks a live connection disconnected, wipes its token and parks
// its maintenance.
func Disconnect(ctx context.Context, tx audit.Tx, brand, id uuid.UUID, now time.Time) error {
	return affected(q(tx.Pgx()).DisconnectConnection(ctx, sqlcgen.DisconnectConnectionParams{WorkspaceID: tx.Scope().WorkspaceID, BrandID: brand, ID: id, Now: now}))
}

func connection(r sqlcgen.GetConnectionRow) Connection {
	return Connection{
		ID: r.ID, BrandID: r.BrandID, GrantID: r.GrantID, Provider: core.Provider(r.Provider), Kind: core.Kind(r.Kind),
		ExternalID: r.ExternalID, DisplayName: r.DisplayName,
		TokenEnc: str(r.TokenEnc), TokenKeyID: str(r.TokenKeyID), Status: core.Status(r.Status),
		TokenExpiresAt: tm(r.TokenExpiresAt), LastCheckedAt: tm(r.LastCheckedAt),
		LastError: core.ErrorCode(str(r.LastError)), WarnedDays: int(r.WarnedDays),
		NextDueAt: tm(r.NextDueAt), CreatedAt: r.CreatedAt,
	}
}

func affected(n int64, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// mapErr turns constraint violations and missing rows into domain errors.
func mapErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "connections_live_external":
			return ErrAlreadyConnected
		case pgErr.Code == "23503":
			return ErrNotFound
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func tm(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.UTC()
}

// Repoint is a reconnected connection's new grant and token.
type Repoint struct {
	GrantID        uuid.UUID
	TokenEnc       string // the account's own token, or empty
	TokenExpiresAt time.Time
	NextDueAt      time.Time
	Now            time.Time
}

// RepointConnection moves a live connection to a new grant and resets its
// health (active, no error, no warnings, checked now).
func RepointConnection(ctx context.Context, tx audit.Tx, brand, id uuid.UUID, p Repoint) error {
	now := p.Now
	return affected(q(tx.Pgx()).RepointConnection(ctx, sqlcgen.RepointConnectionParams{
		GrantID: p.GrantID, TokenEnc: nullString(p.TokenEnc), TokenExpiresAt: nullTime(p.TokenExpiresAt),
		Now: &now, NextDueAt: nullTime(p.NextDueAt), WorkspaceID: tx.Scope().WorkspaceID, BrandID: brand, ID: id,
	}))
}

// LiveConnectionsOnGrant counts the connections still using a grant.
func LiveConnectionsOnGrant(ctx context.Context, tx pgx.Tx, ws, brand, grant uuid.UUID) (int64, error) {
	n, err := q(tx).LiveConnectionsOnGrant(ctx, sqlcgen.LiveConnectionsOnGrantParams{WorkspaceID: ws, BrandID: brand, GrantID: grant})
	return n, mapErr(err)
}
