// Package audit records every write (CLAUDE.md task brief: "audit log for
// every write"). Writes go through Writer.InWriteTx, which commits the audit
// record in the same transaction as the change. Repository write functions
// take an audit.Tx, which only InWriteTx can produce, so an unaudited write
// cannot be expressed.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya31415/tuorps/internal/audit/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

// MaxPageSize bounds list queries.
const MaxPageSize = 100

// ErrPageSize reports a page size outside 1..MaxPageSize.
var ErrPageSize = errors.New("audit: page size must be between 1 and 100")

// Tx is a write transaction whose audit record is guaranteed. Its zero
// value holds no transaction.
type Tx struct {
	tx    pgx.Tx
	scope postgres.Scope
}

// Pgx returns the underlying transaction, for repositories.
func (t Tx) Pgx() pgx.Tx { return t.tx }

// Scope is the tenant scope the transaction runs under.
func (t Tx) Scope() postgres.Scope { return t.scope }

// Writer opens audited write transactions.
type Writer struct {
	pool  *pgxpool.Pool
	newID ids.NewID
	now   clock.Now
}

// NewWriter returns a Writer.
func NewWriter(pool *pgxpool.Pool, newID ids.NewID, now clock.Now) Writer {
	return Writer{pool: pool, newID: newID, now: now}
}

// InWriteTx runs fn in a transaction scoped to s and records ev in it. The
// record is built before fn runs, so an event that cannot be recorded
// performs no work at all.
func (w Writer) InWriteTx(ctx context.Context, s postgres.Scope, ev core.Event, meta core.Meta, fn func(Tx) error) error {
	ins, err := w.prepare(s, ev, meta)
	if err != nil {
		return err
	}
	return postgres.InTx(ctx, w.pool, s, func(tx pgx.Tx) error {
		if err := fn(Tx{tx: tx, scope: s}); err != nil {
			return err
		}
		return ins(ctx, tx)
	})
}

// InWriteTxResolving is InWriteTx for actions whose scope is only known
// inside the transaction (redeeming a one-time token, accepting an
// invitation). resolve runs with no tenant scope; its result then scopes fn
// and the audit record, all in one transaction.
func (w Writer) InWriteTxResolving(ctx context.Context, resolve func(pgx.Tx) (postgres.Scope, error),
	event func(postgres.Scope) (core.Event, error), meta core.Meta, fn func(Tx) error) error {
	return postgres.InTx(ctx, w.pool, postgres.Scope{}, func(tx pgx.Tx) error {
		s, err := resolve(tx)
		if err != nil {
			return err
		}
		ev, err := event(s)
		if err != nil {
			return err
		}
		ins, err := w.prepare(s, ev, meta)
		if err != nil {
			return err
		}
		if err := postgres.SetScope(ctx, tx, s); err != nil {
			return err
		}
		if err := fn(Tx{tx: tx, scope: s}); err != nil {
			return err
		}
		return ins(ctx, tx)
	})
}

// prepare validates the record up front and returns its insert.
func (w Writer) prepare(s postgres.Scope, ev core.Event, meta core.Meta) (func(context.Context, pgx.Tx) error, error) {
	rec, err := core.NewRecord(w.newID(), s.WorkspaceID, s.UserID, ev, meta, w.now())
	if err != nil {
		return nil, err
	}
	details := []byte("{}")
	if rec.Details != nil {
		if details, err = json.Marshal(rec.Details); err != nil {
			return nil, fmt.Errorf("audit: details: %w", err)
		}
	}
	return func(ctx context.Context, tx pgx.Tx) error {
		if err := sqlcgen.New(tx).InsertAuditRecord(ctx, sqlcgen.InsertAuditRecordParams{
			ID: rec.ID, WorkspaceID: rec.WorkspaceID, ActorUserID: rec.ActorUserID, Action: rec.Action,
			TargetType: rec.TargetType, TargetID: rec.TargetID, RequestID: rec.RequestID, IpHash: rec.IPHash,
			Details: details, At: rec.At,
		}); err != nil {
			return fmt.Errorf("audit: insert: %w", err)
		}
		return nil
	}, nil
}

// ListWorkspace returns up to limit records of s's workspace older than
// before (uuid.Nil for the first page), newest first.
func ListWorkspace(ctx context.Context, pool *pgxpool.Pool, s postgres.Scope, before uuid.UUID, limit int32) ([]core.Record, error) {
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrPageSize
	}
	var out []core.Record
	err := postgres.InReadTx(ctx, pool, s, func(tx pgx.Tx) error {
		rows, err := sqlcgen.New(tx).ListWorkspaceAudit(ctx, sqlcgen.ListWorkspaceAuditParams{
			WorkspaceID: &s.WorkspaceID,
			Before:      cursor(before),
			PageSize:    limit,
		})
		if err != nil {
			return err
		}
		out = make([]core.Record, 0, len(rows))
		for _, r := range rows {
			rec, err := toRecord(r)
			if err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	return out, err
}

// ListAccount returns up to limit of the scoped user's own account-level
// records older than before, newest first.
func ListAccount(ctx context.Context, pool *pgxpool.Pool, s postgres.Scope, before uuid.UUID, limit int32) ([]core.Record, error) {
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrPageSize
	}
	var out []core.Record
	err := postgres.InReadTx(ctx, pool, s, func(tx pgx.Tx) error {
		rows, err := sqlcgen.New(tx).ListAccountAudit(ctx, sqlcgen.ListAccountAuditParams{
			ActorUserID: &s.UserID,
			Before:      cursor(before),
			PageSize:    limit,
		})
		if err != nil {
			return err
		}
		out = make([]core.Record, 0, len(rows))
		for _, r := range rows {
			rec, err := toRecord(r)
			if err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	return out, err
}

// cursor maps "no cursor" to the largest UUID so the first page starts at the top.
func cursor(before uuid.UUID) uuid.UUID {
	if before == uuid.Nil() {
		return uuid.Max()
	}
	return before
}

func toRecord(r sqlcgen.AuditLog) (core.Record, error) {
	var d map[string]string
	if err := json.Unmarshal(r.Details, &d); err != nil {
		return core.Record{}, fmt.Errorf("audit: details: %w", err)
	}
	return core.Record{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ActorUserID: r.ActorUserID, Action: r.Action,
		TargetType: r.TargetType, TargetID: r.TargetID, RequestID: r.RequestID, IPHash: r.IpHash,
		Details: d, At: r.At,
	}, nil
}
