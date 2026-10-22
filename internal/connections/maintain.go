package connections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/mail"
	"net/url"
	"strconv"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/audit"
	auditcore "github.com/yahya31415/tuorps/internal/audit/core"
	"github.com/yahya31415/tuorps/internal/connections/adapters/postgres/sqlcgen"
	"github.com/yahya31415/tuorps/internal/connections/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

// errorRetry is how soon a connection whose platform failed transiently is
// looked at again.
const errorRetry = time.Hour

// Mailer queues an email in an audited write transaction.
type Mailer interface {
	Enqueue(ctx context.Context, tx audit.Tx, m emailcore.Message) error
}

// WorkerDeps are the background jobs' collaborators.
type WorkerDeps struct {
	Pool      *pgxpool.Pool
	Writer    audit.Writer
	Vault     Vault
	Keys      *keyring.Keyring
	Providers map[core.Provider]Provider
	Mailer    Mailer
	Inserter  *jobs.Client // enqueues per-connection jobs from the sweep
	Origin    *url.URL     // links in emails
	Now       clock.Now
	Log       *slog.Logger
}

// MaintainArgs is one connection's maintenance pass.
type MaintainArgs struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	WorkspaceID  uuid.UUID `json:"workspace_id"`
}

// Kind is the River job kind.
func (MaintainArgs) Kind() string { return "connections.maintain" }

// Maintainer runs core.Evaluate's plan for one connection: reseal, refresh,
// check, warn, or mark for re-auth (ADR 0009). It works in the connection's
// workspace scope with no user. Status changes and warnings are audited and
// emailed; routine bookkeeping (check times, resealing) is not audited.
type Maintainer struct {
	river.WorkerDefaults[MaintainArgs]
	WorkerDeps
}

// NewMaintainer returns a Maintainer.
func NewMaintainer(d WorkerDeps) *Maintainer { return &Maintainer{WorkerDeps: d} }

// Timeout bounds one pass (a few platform calls).
func (m *Maintainer) Timeout(*river.Job[MaintainArgs]) time.Duration { return 2 * time.Minute }

// pass is the state one maintenance pass builds up and writes.
type pass struct {
	c            Connection
	g            Grant
	grantChanged bool
	email        emailcore.Kind
	days         int
}

func (m *Maintainer) Work(ctx context.Context, j *river.Job[MaintainArgs]) error {
	scope := postgres.Scope{WorkspaceID: j.Args.WorkspaceID}
	var p pass
	err := postgres.InReadTx(ctx, m.Pool, scope, func(tx pgx.Tx) error {
		r, err := q(tx).GetConnectionByID(ctx, sqlcgen.GetConnectionByIDParams{WorkspaceID: j.Args.WorkspaceID, ID: j.Args.ConnectionID})
		if err != nil {
			return mapErr(err)
		}
		p.c = connection(sqlcgen.GetConnectionRow(r))
		gr, err := q(tx).GrantByID(ctx, sqlcgen.GrantByIDParams{WorkspaceID: j.Args.WorkspaceID, ID: p.c.GrantID})
		if err != nil {
			return mapErr(err)
		}
		p.g = grantFromRow(sqlcgen.GetGrantRow(gr))
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	if p.c.Status == core.StatusDisconnected || p.c.Status == core.StatusNeedsReauth {
		return nil
	}
	before := p.c.Status
	now := m.Now()
	m.run(ctx, &p, core.Evaluate(m.state(p), now).Actions, now)
	next := time.Time{}
	if p.c.Status != core.StatusNeedsReauth {
		next = core.Evaluate(m.state(p), now).NextDue
		if p.c.Status == core.StatusError && next.After(now.Add(errorRetry)) {
			next = now.Add(errorRetry)
		}
	}
	p.c.NextDueAt = next
	return m.save(ctx, scope, p, before, now)
}

// state is the core snapshot of a connection and its grant.
func (m *Maintainer) state(p pass) core.ConnState {
	s := core.ConnState{
		Provider: p.c.Provider, Status: p.c.Status, AccessExpiresAt: p.c.TokenExpiresAt, TokenIssuedAt: p.g.TokenIssuedAt,
		LastCheckedAt: p.c.LastCheckedAt, HasRefreshToken: p.g.RefreshTokenEnc != "", WarnedDays: p.c.WarnedDays,
		ActiveKeyID: m.Keys.ActiveKeyID(),
	}
	if p.c.Kind == core.KindInstagramAccount {
		s.DataAccessExpiresAt = p.g.DataAccessExpiresAt // Page permissions never lose data access
	}
	s.KeyID = s.ActiveKeyID
	for _, enc := range []string{p.c.TokenEnc, p.g.AccessTokenEnc, p.g.RefreshTokenEnc} {
		if sealed, err := keyring.Decode(enc); err == nil && sealed.KeyID != s.ActiveKeyID {
			s.KeyID = sealed.KeyID
		}
	}
	return s
}

// run performs the actions, updating p. It stops once the connection is
// parked for re-auth.
func (m *Maintainer) run(ctx context.Context, p *pass, actions []core.Action, now time.Time) {
	ports, configured := m.Providers[p.c.Provider]
	for _, a := range actions {
		switch a {
		case core.ActionReseal:
			m.reseal(p)
		case core.ActionRefresh:
			if configured && ports.Refresh != nil {
				m.refresh(ctx, p, ports, now)
			}
		case core.ActionCheck:
			if configured && ports.Check != nil {
				m.check(ctx, p, ports, now)
			}
		case core.ActionWarn14, core.ActionWarn3:
			p.c.WarnedDays = map[core.Action]int{core.ActionWarn14: 14, core.ActionWarn3: 3}[a]
			p.email, p.days = emailcore.KindConnectionExpiring, daysUntil(m.state(*p).Deadline(), now)
		case core.ActionMarkReauth:
			m.markReauth(p)
		}
		if p.c.Status == core.StatusNeedsReauth {
			return
		}
	}
}

func daysUntil(t, now time.Time) int {
	return int(math.Ceil(t.Sub(now).Hours() / 24))
}

func (m *Maintainer) markReauth(p *pass) {
	p.c.Status, p.c.LastError = core.StatusNeedsReauth, core.ErrReauthRequired
	p.email = emailcore.KindConnectionNeedsReauth
}

// fail records a failed platform call: an auth failure parks the
// connection; anything else is a transient error retried by the schedule.
func (m *Maintainer) fail(p *pass, err error) {
	if errors.Is(err, core.ErrReauthRequired) {
		m.markReauth(p)
		return
	}
	code := core.ErrRejected
	_ = errors.As(err, &code)
	p.c.Status, p.c.LastError = core.StatusError, code
}

func (m *Maintainer) healthy(p *pass, now time.Time) {
	p.c.Status, p.c.LastError, p.c.LastCheckedAt = core.StatusActive, "", now
}

func (m *Maintainer) reseal(p *pass) {
	if enc, changed, err := m.Vault.Reseal(p.c.ID, core.FieldPageToken, p.c.TokenEnc); err == nil && changed {
		p.c.TokenEnc = enc
	}
	for _, f := range []struct {
		field core.TokenField
		enc   *string
	}{{core.FieldAccessToken, &p.g.AccessTokenEnc}, {core.FieldRefreshToken, &p.g.RefreshTokenEnc}} {
		if *f.enc == "" {
			continue
		}
		if enc, changed, err := m.Vault.Reseal(p.g.ID, f.field, *f.enc); err == nil && changed {
			*f.enc, p.grantChanged = enc, true
		}
	}
}

// renew refreshes the grant's access token and stores the result in p.
func (m *Maintainer) renew(ctx context.Context, p *pass, ports Provider, now time.Time) (string, error) {
	field, enc := core.FieldAccessToken, p.g.AccessTokenEnc
	if p.g.RefreshTokenEnc != "" {
		field, enc = core.FieldRefreshToken, p.g.RefreshTokenEnc
	}
	tok, err := m.Vault.Open(p.g.ID, field, enc)
	if err != nil {
		return "", err
	}
	ts, err := ports.Refresh(ctx, tok)
	if err != nil {
		return "", err
	}
	if p.g.AccessTokenEnc, err = m.Vault.Seal(p.g.ID, core.FieldAccessToken, ts.AccessToken()); err != nil {
		return "", err
	}
	if ts.RefreshToken() != "" {
		if p.g.RefreshTokenEnc, err = m.Vault.Seal(p.g.ID, core.FieldRefreshToken, ts.RefreshToken()); err != nil {
			return "", err
		}
	}
	p.g.TokenIssuedAt, p.g.AccessExpiresAt = now, time.Time{}
	if ts.ExpiresIn() > 0 {
		p.g.AccessExpiresAt = now.Add(ts.ExpiresIn())
	}
	if p.c.TokenEnc == "" && p.c.Provider != core.ProviderGoogle {
		p.c.TokenExpiresAt = p.g.AccessExpiresAt // the connection uses the grant's token
	}
	p.grantChanged = true
	return ts.AccessToken(), nil
}

func (m *Maintainer) refresh(ctx context.Context, p *pass, ports Provider, now time.Time) {
	if _, err := m.renew(ctx, p, ports, now); err != nil {
		m.fail(p, err)
		return
	}
	m.healthy(p, now)
}

// check asks the platform whether the connection's token still works.
// Google access tokens last an hour, so Google renews first.
func (m *Maintainer) check(ctx context.Context, p *pass, ports Provider, now time.Time) {
	var tok string
	var err error
	switch {
	case p.c.Provider == core.ProviderGoogle && p.g.RefreshTokenEnc != "" && ports.Refresh != nil:
		tok, err = m.renew(ctx, p, ports, now)
	case p.c.TokenEnc != "":
		tok, err = m.Vault.Open(p.c.ID, core.FieldPageToken, p.c.TokenEnc)
	default:
		tok, err = m.Vault.Open(p.g.ID, core.FieldAccessToken, p.g.AccessTokenEnc)
	}
	if errors.Is(err, ErrNoToken) {
		m.markReauth(p)
		return
	}
	if err != nil {
		m.fail(p, err)
		return
	}
	h, err := ports.Check(ctx, tok)
	switch {
	case err != nil:
		m.fail(p, err)
	case !h.Valid:
		m.markReauth(p)
	default:
		m.healthy(p, now)
		if !h.DataAccessExpiresAt.IsZero() && !h.DataAccessExpiresAt.Equal(p.g.DataAccessExpiresAt) {
			p.g.DataAccessExpiresAt, p.grantChanged = h.DataAccessExpiresAt, true
		}
	}
}

// save writes the pass. Status changes and warnings are audited and
// emailed in one transaction; other bookkeeping is a plain scoped write.
func (m *Maintainer) save(ctx context.Context, scope postgres.Scope, p pass, before core.Status, now time.Time) error {
	write := func(tx pgx.Tx) error {
		if _, err := q(tx).UpdateConnectionHealth(ctx, sqlcgen.UpdateConnectionHealthParams{
			Status: string(p.c.Status), LastError: nullString(string(p.c.LastError)), LastCheckedAt: nullTime(p.c.LastCheckedAt),
			WarnedDays: int16(p.c.WarnedDays), NextDueAt: nullTime(p.c.NextDueAt), TokenEnc: nullString(p.c.TokenEnc),
			TokenExpiresAt: nullTime(p.c.TokenExpiresAt), UpdatedAt: now, WorkspaceID: scope.WorkspaceID, ID: p.c.ID,
		}); err != nil {
			return err
		}
		if !p.grantChanged {
			return nil
		}
		_, err := q(tx).UpdateGrantTokens(ctx, sqlcgen.UpdateGrantTokensParams{
			AccessTokenEnc: nullString(p.g.AccessTokenEnc), RefreshTokenEnc: nullString(p.g.RefreshTokenEnc),
			AccessExpiresAt: nullTime(p.g.AccessExpiresAt), TokenIssuedAt: nullTime(p.g.TokenIssuedAt),
			DataAccessExpiresAt: nullTime(p.g.DataAccessExpiresAt), UpdatedAt: now,
			WorkspaceID: scope.WorkspaceID, BrandID: p.c.BrandID, ID: p.g.ID,
		})
		return err
	}
	if p.c.Status == before && p.email == "" {
		return postgres.InTx(ctx, m.Pool, scope, write)
	}
	ev := statusEvent(p, before)
	return m.Writer.InWriteTx(ctx, scope, ev, auditcore.Meta{}, func(tx audit.Tx) error {
		if err := write(tx.Pgx()); err != nil {
			return err
		}
		if p.email == "" {
			return nil
		}
		return m.notify(ctx, tx, p)
	})
}

func statusEvent(p pass, before core.Status) auditcore.Event {
	details := map[string]string{"from": string(before), "to": string(p.c.Status)}
	if p.c.LastError != "" {
		details["reason"] = string(p.c.LastError)
	}
	action := "connection.status"
	if p.c.Status == before {
		action, details = "connection.expiry_warning", map[string]string{"days": strconv.Itoa(p.days)}
	}
	ev, _ := auditcore.NewEvent(action, "connection", p.c.ID.String(), details)
	return ev
}

// notify emails the workspace owners and the brand's admins.
func (m *Maintainer) notify(ctx context.Context, tx audit.Tx, p pass) error {
	rows, err := q(tx.Pgx()).Recipients(ctx, sqlcgen.RecipientsParams{BrandID: p.c.BrandID, WorkspaceID: tx.Scope().WorkspaceID})
	if err != nil {
		return err
	}
	link := emailcore.Link(m.Origin, "/connections", map[string]string{
		"workspace": tx.Scope().WorkspaceID.String(), "brand": p.c.BrandID.String(), "connection": p.c.ID.String(),
	})
	for _, r := range rows {
		data := map[string]string{"name": r.DisplayName, "account": p.c.DisplayName, "platform": platformName(p.c), "link": link}
		if p.email == emailcore.KindConnectionExpiring {
			data["days"] = strconv.Itoa(p.days)
		}
		msg, err := emailcore.NewMessage(mail.Address{Name: r.DisplayName, Address: r.Email}, p.email, r.Locale, data)
		if err != nil {
			return fmt.Errorf("connections: email: %w", err)
		}
		if err := m.Mailer.Enqueue(ctx, tx, msg); err != nil {
			return err
		}
	}
	return nil
}

// platformName is the platform as customers know it.
func platformName(c Connection) string {
	switch c.Kind {
	case core.KindFacebookPage:
		return "Facebook"
	case core.KindInstagramAccount:
		return "Instagram"
	case core.KindLinkedInOrg:
		return "LinkedIn"
	case core.KindYouTubeChannel:
		return "YouTube"
	}
	return "WhatsApp"
}

func grantFromRow(r sqlcgen.GetGrantRow) Grant {
	return Grant{
		ID: r.ID, BrandID: r.BrandID, Provider: core.Provider(r.Provider), ProviderUserID: r.ProviderUserID,
		Scopes: r.Scopes, MissingScopes: r.MissingScopes,
		AccessTokenEnc: str(r.AccessTokenEnc), RefreshTokenEnc: str(r.RefreshTokenEnc),
		AccessExpiresAt: tm(r.AccessExpiresAt), RefreshExpiresAt: tm(r.RefreshExpiresAt),
		DataAccessExpiresAt: tm(r.DataAccessExpiresAt), TokenIssuedAt: tm(r.TokenIssuedAt),
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}
}
