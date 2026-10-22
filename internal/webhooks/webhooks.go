// Package webhooks ingests platform webhooks (ADR 0010): a verified
// delivery is recorded for deduplication and enqueued, sealed, in one
// transaction. Processing happens in the worker (webhooks.process).
package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/webhooks/adapters/postgres/sqlcgen"
)

// ProcessAAD binds sealed job payloads to the webhooks.process kind.
const ProcessAAD = "jobs/webhooks.process"

const maxAttempts = 12

// Event is what a delivery reports.
type Event string

const (
	EventNotification Event = "notification"  // a webhook payload (raw body)
	EventDeauthorize  Event = "deauthorize"   // a user removed the app
	EventDataDeletion Event = "data_deletion" // a user asked for their data to be deleted
)

// ProcessArgs is the webhooks.process job. Payload is sealed: it can hold
// messages, comments and platform user ids.
type ProcessArgs struct {
	Provider string `json:"provider"`
	Event    Event  `json:"event"`
	Sealed   string `json:"sealed"`
}

// Kind is the River job kind.
func (ProcessArgs) Kind() string { return "webhooks.process" }

// Deletion is the sealed payload of a data_deletion event.
type Deletion struct {
	ProviderUserID   string `json:"provider_user_id"`
	ConfirmationCode string `json:"confirmation_code"`
}

// Inbox writes deliveries.
type Inbox struct {
	pool     *pgxpool.Pool
	inserter *jobs.Client
	keys     *keyring.Keyring
	rand     io.Reader
}

// NewInbox returns an Inbox; rand supplies encryption nonces.
func NewInbox(pool *pgxpool.Pool, inserter *jobs.Client, keys *keyring.Keyring, rand io.Reader) Inbox {
	return Inbox{pool: pool, inserter: inserter, keys: keys, rand: rand}
}

// DedupeKey identifies a delivery: the SHA-256 of its exact bytes, so
// platform retries of the same delivery collapse (ADR 0010).
func DedupeKey(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

// Ingest records a verified delivery and enqueues it, once: a repeat of the
// same bytes from the same provider is acknowledged and dropped. It reports
// whether the delivery was new.
func (in Inbox) Ingest(ctx context.Context, p core.Provider, e Event, payload []byte, now time.Time) (bool, error) {
	fresh := false
	err := postgres.InTx(ctx, in.pool, postgres.Scope{}, func(tx pgx.Tx) error {
		n, err := sqlcgen.New(tx).InsertReceipt(ctx, sqlcgen.InsertReceiptParams{Provider: string(p), DedupeKey: DedupeKey(append([]byte(e+":"), payload...)), ReceivedAt: now})
		if err != nil || n == 0 {
			return err
		}
		fresh = true
		return in.enqueue(ctx, tx, p, e, payload)
	})
	if err != nil {
		return false, fmt.Errorf("webhooks: ingest: %w", err)
	}
	return fresh, nil
}

// RequestDeletion records a data-deletion request under code and enqueues
// the deletion.
func (in Inbox) RequestDeletion(ctx context.Context, p core.Provider, providerUserID, code string, now time.Time) error {
	payload, err := json.Marshal(Deletion{ProviderUserID: providerUserID, ConfirmationCode: code})
	if err != nil {
		return fmt.Errorf("webhooks: deletion: %w", err)
	}
	err = postgres.InTx(ctx, in.pool, postgres.Scope{}, func(tx pgx.Tx) error {
		if err := sqlcgen.New(tx).InsertDeletionRequest(ctx, sqlcgen.InsertDeletionRequestParams{
			ConfirmationCode: code, Provider: string(p), ProviderUserID: providerUserID, RequestedAt: now,
		}); err != nil {
			return err
		}
		return in.enqueue(ctx, tx, p, EventDataDeletion, payload)
	})
	if err != nil {
		return fmt.Errorf("webhooks: deletion: %w", err)
	}
	return nil
}

// DeletionStatus reports a data-deletion request's status ("pending",
// "done"), or false for an unknown code.
func (in Inbox) DeletionStatus(ctx context.Context, code string) (string, bool, error) {
	var status string
	err := postgres.InReadTx(ctx, in.pool, postgres.Scope{}, func(tx pgx.Tx) error {
		var err error
		status, err = sqlcgen.New(tx).DeletionStatus(ctx, code)
		return err
	})
	if err != nil {
		return "", false, fmt.Errorf("webhooks: deletion status: %w", err)
	}
	return status, status != "", nil
}

func (in Inbox) enqueue(ctx context.Context, tx pgx.Tx, p core.Provider, e Event, payload []byte) error {
	sealed, err := in.keys.Seal(in.rand, payload, []byte(ProcessAAD))
	if err != nil {
		return err
	}
	_, err = in.inserter.InsertTx(ctx, tx, ProcessArgs{Provider: string(p), Event: e, Sealed: sealed.Encode()}, &river.InsertOpts{MaxAttempts: maxAttempts})
	return err
}
