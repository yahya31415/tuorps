// Package email queues and delivers transactional email. Enqueue inserts a
// River job in the caller's audited write transaction (a transactional
// outbox: the email exists only if the write commits). Job arguments are
// encrypted with the keyring because they carry recipients and one-time
// tokens; the worker decrypts, renders and sends over SMTP.
package email

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/email/adapters"
	"github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

const (
	// SendArgsAAD binds sealed job payloads to this job kind.
	SendArgsAAD = "jobs/email.send"
	sendTimeout = 30 * time.Second
	maxAttempts = 8
)

// SendArgs are the River job arguments: ciphertext only.
type SendArgs struct {
	Sealed string `json:"sealed"`
}

// Kind is the River job kind.
func (SendArgs) Kind() string { return "email.send" }

// Sender delivers a complete RFC 5322 message.
type Sender interface {
	Send(ctx context.Context, env adapters.Envelope, msg []byte) error
}

// Outbox enqueues emails inside audited write transactions.
type Outbox struct {
	inserter *jobs.Client
	keys     *keyring.Keyring
	rand     io.Reader
}

// NewOutbox returns an Outbox; rand supplies encryption nonces.
func NewOutbox(inserter *jobs.Client, keys *keyring.Keyring, rand io.Reader) Outbox {
	return Outbox{inserter: inserter, keys: keys, rand: rand}
}

// Enqueue schedules m for delivery when tx commits.
func (o Outbox) Enqueue(ctx context.Context, tx audit.Tx, m core.Message) error {
	return o.enqueue(ctx, tx.Pgx(), m)
}

// EnqueueSystem schedules an operator email (such as a version-sunset
// alert) in a plain transaction: it concerns no workspace or user, so there
// is nothing to audit. Customer email goes through Enqueue.
func (o Outbox) EnqueueSystem(ctx context.Context, tx pgx.Tx, m core.Message) error {
	return o.enqueue(ctx, tx, m)
}

func (o Outbox) enqueue(ctx context.Context, tx pgx.Tx, m core.Message) error {
	payload, err := m.MarshalJSON()
	if err != nil {
		return fmt.Errorf("email: encode: %w", err)
	}
	sealed, err := o.keys.Seal(o.rand, payload, []byte(SendArgsAAD))
	if err != nil {
		return fmt.Errorf("email: seal: %w", err)
	}
	if _, err := o.inserter.InsertTx(ctx, tx, SendArgs{Sealed: sealed.Encode()}, &river.InsertOpts{MaxAttempts: maxAttempts}); err != nil {
		return fmt.Errorf("email: enqueue: %w", err)
	}
	return nil
}

// Worker delivers email jobs.
type Worker struct {
	river.WorkerDefaults[SendArgs]
	keys   *keyring.Keyring
	sender Sender
	from   mail.Address
	newID  ids.NewID
	now    clock.Now
}

// NewWorker returns the email worker.
func NewWorker(keys *keyring.Keyring, sender Sender, from mail.Address, newID ids.NewID, now clock.Now) *Worker {
	return &Worker{keys: keys, sender: sender, from: from, newID: newID, now: now}
}

// Register adds w to workers.
func Register(workers *river.Workers, w *Worker) { river.AddWorker(workers, w) }

// Timeout bounds one delivery attempt.
func (w *Worker) Timeout(*river.Job[SendArgs]) time.Duration { return sendTimeout }

// Work decrypts, renders and sends one email. Payloads that cannot be
// decrypted or validated are cancelled (retrying cannot help); delivery
// failures are returned so River retries with backoff.
func (w *Worker) Work(ctx context.Context, j *river.Job[SendArgs]) error {
	sealed, err := keyring.Decode(j.Args.Sealed)
	if err != nil {
		return river.JobCancel(fmt.Errorf("email: payload: %w", err))
	}
	payload, err := w.keys.Open(sealed, []byte(SendArgsAAD))
	if err != nil {
		return river.JobCancel(fmt.Errorf("email: payload: %w", err))
	}
	m, err := core.ParseMessage(payload)
	if err != nil {
		return river.JobCancel(err)
	}
	r, err := core.Render(m)
	if err != nil {
		return river.JobCancel(err)
	}
	id := w.newID()
	raw, err := core.BuildMIME(w.from, m, r, "<"+id.String()+"@"+domain(w.from.Address)+">", w.now(), hex.EncodeToString(id[:]))
	if err != nil {
		return river.JobCancel(err)
	}
	if err := w.sender.Send(ctx, adapters.Envelope{From: w.from.Address, To: m.To().Address}, raw); err != nil {
		return fmt.Errorf("email: send (attempt %d): %w", j.Attempt, err)
	}
	return nil
}

func domain(addr string) string {
	if _, d, ok := strings.Cut(addr, "@"); ok {
		return d
	}
	return "tuorps.invalid"
}
