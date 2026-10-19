package email_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/email"
	"github.com/yahya31415/tuorps/internal/email/adapters"
	"github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

const userID = "0199c3f0-0000-7000-8000-0000000000a1"

func testKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New(map[string][]byte{"k1": make([]byte, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func from() mail.Address { return mail.Address{Name: "Tuorps", Address: "no-reply@tuorps.in"} }

func resetMessage(t *testing.T) core.Message {
	t.Helper()
	m, err := core.NewMessage(mail.Address{Name: "Priya", Address: "priya@example.in"}, core.KindPasswordReset, "en",
		map[string]string{"name": "Priya", "link": "https://app.tuorps.in/reset-password#token=SECRET-TOKEN-123"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type fakeSender struct {
	mu   sync.Mutex
	sent []adapters.Envelope
	raw  [][]byte
	err  error
}

func (f *fakeSender) Send(_ context.Context, e adapters.Envelope, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, e)
	f.raw = append(f.raw, msg)
	return nil
}

func job(args email.SendArgs) *river.Job[email.SendArgs] {
	return &river.Job[email.SendArgs]{JobRow: &rivertype.JobRow{ID: 1, Attempt: 1}, Args: args}
}

func sealedArgs(t *testing.T, k *keyring.Keyring, payload []byte, aad string) email.SendArgs {
	t.Helper()
	s, err := k.Seal(rand.Reader, payload, []byte(aad))
	if err != nil {
		t.Fatal(err)
	}
	return email.SendArgs{Sealed: s.Encode()}
}

func isCancel(err error) bool {
	var c *rivertype.JobCancelError
	return errors.As(err, &c)
}

func worker(k *keyring.Keyring, s email.Sender) *email.Worker {
	return email.NewWorker(k, s, from(), ids.System(), clock.Fixed(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)))
}

func TestWorkerSendsTheDecryptedMessage(t *testing.T) {
	k := testKeyring(t)
	payload, _ := resetMessage(t).MarshalJSON()
	s := &fakeSender{}
	if err := worker(k, s).Work(t.Context(), job(sealedArgs(t, k, payload, email.SendArgsAAD))); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0].To != "priya@example.in" || s.sent[0].From != "no-reply@tuorps.in" {
		t.Fatalf("envelopes = %+v", s.sent)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(s.raw[0])))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("Subject") != "Reset your Tuorps password" || !strings.HasSuffix(m.Header.Get("Message-Id"), "@tuorps.in>") {
		t.Errorf("headers = %v", m.Header)
	}
	if d, _ := m.Header.Date(); !d.Equal(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v", d)
	}
}

func TestWorkerCancelsUnusablePayloads(t *testing.T) {
	k := testKeyring(t)
	good, _ := resetMessage(t).MarshalJSON()
	cases := map[string]email.SendArgs{
		"not a sealed value":   {Sealed: "garbage"},
		"sealed for elsewhere": sealedArgs(t, k, good, "users/42/totp"),
		"invalid message":      sealedArgs(t, k, []byte(`{"to":{"Address":"p@example.in"},"kind":"spam"}`), email.SendArgsAAD),
		"tampered ciphertext": func() email.SendArgs {
			a := sealedArgs(t, k, good, email.SendArgsAAD)
			return email.SendArgs{Sealed: a.Sealed[:len(a.Sealed)-4] + "AAAA"}
		}(),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			s := &fakeSender{}
			err := worker(k, s).Work(t.Context(), job(args))
			if !isCancel(err) {
				t.Fatalf("err = %v, want a permanent cancellation", err)
			}
			if len(s.sent) != 0 {
				t.Error("sent anyway")
			}
		})
	}
}

func TestWorkerRetriesTransientSMTPFailures(t *testing.T) {
	k := testKeyring(t)
	payload, _ := resetMessage(t).MarshalJSON()
	s := &fakeSender{err: errors.New("421 try again later")}
	err := worker(k, s).Work(t.Context(), job(sealedArgs(t, k, payload, email.SendArgsAAD)))
	if err == nil || isCancel(err) {
		t.Fatalf("err = %v, want a retryable error", err)
	}
}

func TestWorkerHasABoundedTimeout(t *testing.T) {
	if d := worker(testKeyring(t), &fakeSender{}).Timeout(job(email.SendArgs{})); d <= 0 || d > time.Minute {
		t.Errorf("Timeout = %v", d)
	}
}

func TestOutboxDeliversOnlyCommittedEmailsAndStoresNoPlaintext(t *testing.T) {
	pg := testinfra.MigratedPostgres(t)
	mp := testinfra.StartMailpit(t, testinfra.MailpitOptions{})
	mk := func(url string) *pgxpool.Pool {
		p, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(url), MaxConns: 5, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	migrator, app, admin := mk(pg.MigratorURL), mk(pg.AppURL), mk(pg.AdminURL)
	log := slog.New(slog.DiscardHandler)
	if err := jobs.Migrate(t.Context(), migrator, log); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO tuorps.users (id, email, email_normalized, display_name, created_at, updated_at) VALUES ($1, 'priya@example.in', 'priya@example.in', 'Priya', now(), now())`, userID); err != nil {
		t.Fatal(err)
	}

	k := testKeyring(t)
	sender := adapters.NewSMTP(config.SMTP{Host: mp.SMTPHost, Port: mp.SMTPPort, TLS: config.SMTPNoTLS, Timeout: 5 * time.Second}, nil)
	workers := river.NewWorkers()
	email.Register(workers, worker(k, sender))
	runner, err := jobs.NewWorker(app, workers, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	})
	inserter, err := jobs.NewInserter(app, log)
	if err != nil {
		t.Fatal(err)
	}
	outbox := email.NewOutbox(inserter, k, rand.Reader)
	w := audit.NewWriter(app, ids.System(), clock.System())
	scope := postgres.Scope{UserID: uuid.MustParse(userID)}
	ev, _ := auditcore.NewEvent("auth.password_reset_request", "user", userID, nil)

	if err := w.InWriteTx(t.Context(), scope, ev, auditcore.Meta{}, func(tx audit.Tx) error {
		return outbox.Enqueue(t.Context(), tx, resetMessage(t))
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	boom := errors.New("rollback")
	rolledBack, _ := core.NewMessage(mail.Address{Address: "priya@example.in"}, core.KindPasswordChanged, "en", map[string]string{"name": "ROLLED-BACK"})
	_ = w.InWriteTx(t.Context(), scope, ev, auditcore.Meta{}, func(tx audit.Tx) error {
		if err := outbox.Enqueue(t.Context(), tx, rolledBack); err != nil {
			return err
		}
		return boom
	})

	var subjects []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(subjects) == 0 {
		subjects = mailpitSubjects(t, mp)
		time.Sleep(200 * time.Millisecond)
	}
	if len(subjects) != 1 || subjects[0] != "Reset your Tuorps password" {
		t.Fatalf("delivered subjects = %v, want only the committed email", subjects)
	}

	rows, err := admin.Query(t.Context(), `SELECT args::text FROM tuorps.river_job WHERE kind = $1`, email.SendArgs{}.Kind())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var args string
		_ = rows.Scan(&args)
		n++
		for _, plain := range []string{"priya@example.in", "SECRET-TOKEN-123", "Priya", "password_reset"} {
			if strings.Contains(args, plain) {
				t.Errorf("job args contain %q in plaintext: %s", plain, args)
			}
		}
	}
	if n != 1 {
		t.Errorf("email jobs = %d, want 1 (the rolled-back one must not exist)", n)
	}
	time.Sleep(time.Second)
	if got := mailpitSubjects(t, mp); len(got) != 1 {
		t.Errorf("subjects later = %v", got)
	}
}

func mailpitSubjects(t *testing.T, mp *testinfra.Mailpit) []string {
	t.Helper()
	resp, err := http.Get(mp.APIURL + "/api/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct{ Messages []struct{ Subject string } }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	var out []string
	for _, m := range body.Messages {
		out = append(out, m.Subject)
	}
	return out
}
