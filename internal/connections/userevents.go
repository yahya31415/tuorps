package connections

import (
	"context"
	"slices"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/connections/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/connections/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

type grantRef struct{ id, ws uuid.UUID }

// grantsOfUser finds the grants a platform user's callback concerns, in
// every workspace. For Instagram Login the callback's id may be the
// Instagram-scoped user id (the grant's) or the account id (a connection's
// external id), so both are matched (ADR 0010).
func grantsOfUser(ctx context.Context, d WorkerDeps, p core.Provider, userID string) ([]grantRef, error) {
	var refs []grantRef
	var byAccount []sqlcgen.ConnectionsByExternalRow
	if err := postgres.InReadTx(ctx, d.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		rows, err := q(tx).GrantsByProviderUser(ctx, sqlcgen.GrantsByProviderUserParams{Provider: string(p), ProviderUserID: userID})
		for _, r := range rows {
			refs = append(refs, grantRef{r.ID, r.WorkspaceID})
		}
		if err != nil || p != core.ProviderInstagram {
			return err
		}
		byAccount, err = q(tx).ConnectionsByExternal(ctx, sqlcgen.ConnectionsByExternalParams{Kind: string(core.KindInstagramAccount), ExternalID: userID})
		return err
	}); err != nil {
		return nil, err
	}
	for _, c := range byAccount {
		if err := postgres.InReadTx(ctx, d.Pool, postgres.Scope{WorkspaceID: c.WorkspaceID}, func(tx pgx.Tx) error {
			r, err := q(tx).GetConnectionByID(ctx, sqlcgen.GetConnectionByIDParams{WorkspaceID: c.WorkspaceID, ID: c.ID})
			if err == nil && r.Provider == string(p) {
				refs = append(refs, grantRef{r.GrantID, c.WorkspaceID})
			}
			return mapErr(err)
		}); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(refs, func(a, b grantRef) int { return a.id.Compare(b.id) })
	return slices.Compact(refs), nil
}

// forEachGrant runs fn for each grant of the user, in an audited write in
// the grant's workspace, with its live connections.
func forEachGrant(ctx context.Context, d WorkerDeps, p core.Provider, userID, action string, fn func(tx audit.Tx, g Grant, live []Connection, now time.Time) error) error {
	refs, err := grantsOfUser(ctx, d, p, userID)
	if err != nil {
		return err
	}
	now := d.Now()
	for _, ref := range refs {
		ev, _ := auditcore.NewEvent(action, "oauth_grant", ref.id.String(), map[string]string{"provider": string(p)})
		if err := d.Writer.InWriteTx(ctx, postgres.Scope{WorkspaceID: ref.ws}, ev, auditcore.Meta{}, func(tx audit.Tx) error {
			gr, err := q(tx.Pgx()).GrantByID(ctx, sqlcgen.GrantByIDParams{WorkspaceID: ref.ws, ID: ref.id})
			if err != nil {
				return mapErr(err)
			}
			rows, err := q(tx.Pgx()).GrantConnections(ctx, sqlcgen.GrantConnectionsParams{WorkspaceID: ref.ws, GrantID: ref.id})
			if err != nil {
				return err
			}
			live := make([]Connection, 0, len(rows))
			for _, r := range rows {
				live = append(live, Connection{ID: r.ID, BrandID: r.BrandID, GrantID: ref.id, Provider: p, Kind: core.Kind(r.Kind), DisplayName: r.DisplayName, Status: core.Status(r.Status)})
			}
			return fn(tx, grantFromRow(sqlcgen.GetGrantRow(gr)), live, now)
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeauthorizeUser handles a deauthorize callback: the user removed the
// app, so every grant of theirs stops working. Their connections are
// marked for re-auth (emailing the brand's admins) and the tokens wiped.
func DeauthorizeUser(ctx context.Context, d WorkerDeps, p core.Provider, userID string) error {
	return forEachGrant(ctx, d, p, userID, "connection.deauthorized", func(tx audit.Tx, g Grant, live []Connection, now time.Time) error {
		for _, c := range live {
			if _, err := q(tx.Pgx()).UpdateConnectionHealth(ctx, sqlcgen.UpdateConnectionHealthParams{
				Status: string(core.StatusNeedsReauth), LastError: nullString(string(core.ErrReauthRequired)), LastCheckedAt: &now,
				UpdatedAt: now, WorkspaceID: tx.Scope().WorkspaceID, ID: c.ID,
			}); err != nil {
				return err
			}
			if err := notify(ctx, d, tx, c, emailcore.KindConnectionNeedsReauth, 0); err != nil {
				return err
			}
		}
		return WipeGrantTokens(ctx, tx, g.BrandID, g.ID, now)
	})
}

// DeleteUserData handles a data deletion request: every grant of the user
// has its tokens wiped and its connections disconnected; then the request
// identified by code is marked done.
func DeleteUserData(ctx context.Context, d WorkerDeps, p core.Provider, userID, code string) error {
	if err := forEachGrant(ctx, d, p, userID, "connection.data_deletion", func(tx audit.Tx, g Grant, live []Connection, now time.Time) error {
		for _, c := range live {
			if err := Disconnect(ctx, tx, c.BrandID, c.ID, now); err != nil {
				return err
			}
		}
		return WipeGrantTokens(ctx, tx, g.BrandID, g.ID, now)
	}); err != nil {
		return err
	}
	return postgres.InTx(ctx, d.Pool, postgres.Scope{}, func(tx pgx.Tx) error {
		_, err := q(tx).CompleteDeletion(ctx, sqlcgen.CompleteDeletionParams{ConfirmationCode: code, Now: d.Now()})
		return err
	})
}
