package connections_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/audit"
	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/email"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
	"github.com/yahya31415/tuorps/internal/platform/clock"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/ids"
	"github.com/yahya31415/tuorps/internal/platform/jobs"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/platform/postgres"
)

// fakePlatform records calls and answers from a script.
type fakePlatform struct {
	checked, refreshed []string
	check              func(token string) (core.Health, error)
	refresh            func(token string) (core.TokenSet, error)
}

func (f *fakePlatform) ports() connections.Provider {
	return connections.Provider{
		Check: func(_ context.Context, tok string) (core.Health, error) {
			f.checked = append(f.checked, tok)
			return f.check(tok)
		},
		Refresh: func(_ context.Context, tok string) (core.TokenSet, error) {
			f.refreshed = append(f.refreshed, tok)
			return f.refresh(tok)
		},
	}
}

type maintFixture struct {
	fixture
	keys  *keyring.Keyring
	vault connections.Vault
	m     *connections.Maintainer
	plat  map[core.Provider]*fakePlatform
}

func newMaintFixture(t *testing.T) *maintFixture {
	t.Helper()
	f := newFixture(t)
	migrator, err := postgres.NewPool(t.Context(), config.DB{URL: config.NewSecret(f.pg.MigratorURL), MaxConns: 2, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	if err := jobs.Migrate(t.Context(), migrator, log); err != nil {
		t.Fatal(err)
	}
	migrator.Close()
	inserter, err := jobs.NewInserter(f.pool, log)
	if err != nil {
		t.Fatal(err)
	}
	keys := testKeyring(t, "k1")
	mf := &maintFixture{fixture: f, keys: keys, vault: connections.NewVault(keys, rand.Reader), plat: map[core.Provider]*fakePlatform{}}
	ports := map[core.Provider]connections.Provider{}
	for _, p := range []core.Provider{core.ProviderMeta, core.ProviderInstagram, core.ProviderLinkedIn, core.ProviderGoogle} {
		fp := &fakePlatform{
			check:   func(string) (core.Health, error) { return core.Health{Valid: true}, nil },
			refresh: func(string) (core.TokenSet, error) { return core.TokenSet{}, errors.New("unexpected refresh") },
		}
		mf.plat[p] = fp
		ports[p] = fp.ports()
	}
	origin, _ := url.Parse("https://app.tuorps.test")
	mf.m = connections.NewMaintainer(connections.WorkerDeps{
		Pool: f.pool, Writer: f.w, Vault: mf.vault, Keys: keys, Providers: ports,
		Mailer: email.NewOutbox(inserter, keys, rand.Reader), Origin: origin, Now: clock.Fixed(f.now), Log: log,
	})
	return mf
}

// seed stores a grant (tokens sealed with sealKeys) and one connection
// using it, then applies sql (with $1 = connection id) as the superuser.
func (mf *maintFixture) seed(t *testing.T, p core.Provider, kind core.Kind, sealWith *keyring.Keyring, grantTok, refreshTok, pageTok string, adjust string) (grant, conn uuid.UUID) {
	t.Helper()
	grant, conn = ids.System()(), ids.System()()
	v := connections.NewVault(sealWith, rand.Reader)
	g := connections.NewGrant{ID: grant, BrandID: brA1(), Provider: p, ProviderUserID: "u-" + string(p), CreatedBy: ownerA(), TokenIssuedAt: mf.now, Now: mf.now}
	g.AccessTokenEnc, _ = v.Seal(grant, core.FieldAccessToken, grantTok)
	if refreshTok != "" {
		g.RefreshTokenEnc, _ = v.Seal(grant, core.FieldRefreshToken, refreshTok)
	}
	c := connections.NewConnection{ID: conn, BrandID: brA1(), GrantID: grant, Kind: kind, ExternalID: "ext-" + conn.String(), DisplayName: "Kerala Spices", NextDueAt: mf.now, Now: mf.now}
	if pageTok != "" {
		c.TokenEnc, _ = v.Seal(conn, core.FieldPageToken, pageTok)
	}
	if err := mf.write(t, scope(wsA(), ownerA()), func(tx audit.Tx) error {
		if err := connections.InsertGrant(t.Context(), tx, g); err != nil {
			return err
		}
		return connections.InsertConnection(t.Context(), tx, c)
	}); err != nil {
		t.Fatal(err)
	}
	if adjust != "" {
		args := []any{conn, grant}[:strings.Count(adjust, "$")]
		if _, err := mf.admin.Exec(t.Context(), adjust, args...); err != nil {
			t.Fatalf("adjust: %v", err)
		}
	}
	return grant, conn
}

func (mf *maintFixture) work(t *testing.T, ws, conn uuid.UUID) error {
	t.Helper()
	return mf.m.Work(t.Context(), &river.Job[connections.MaintainArgs]{Args: connections.MaintainArgs{ConnectionID: conn, WorkspaceID: ws}})
}

func (mf *maintFixture) conn(t *testing.T, id uuid.UUID) connections.Connection {
	t.Helper()
	var c connections.Connection
	if err := mf.read(t, scope(wsA(), ownerA()), func(tx pgxTx) error {
		var err error
		c, err = connections.GetConnection(t.Context(), tx, wsA(), brA1(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// emails decrypts the queued emails of kind.
func (mf *maintFixture) emails(t *testing.T, kind emailcore.Kind) []emailcore.Message {
	t.Helper()
	rows, err := mf.admin.Query(t.Context(), `SELECT args FROM tuorps.river_job WHERE kind = 'email.send' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []emailcore.Message
	for rows.Next() {
		var args email.SendArgs
		if err := rows.Scan(&args); err != nil {
			t.Fatal(err)
		}
		s, _ := keyring.Decode(args.Sealed)
		plain, err := mf.keys.Open(s, []byte(email.SendArgsAAD))
		if err != nil {
			t.Fatal(err)
		}
		m, err := emailcore.ParseMessage(plain)
		if err != nil {
			t.Fatal(err)
		}
		if m.Kind() == kind {
			out = append(out, m)
		}
	}
	return out
}

func (mf *maintFixture) audits(t *testing.T, action string) int {
	t.Helper()
	var n int
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.audit_log WHERE workspace_id = $1 AND action = $2 AND actor_user_id IS NULL`, wsA(), action).Scan(&n)
	return n
}

const day = 24 * time.Hour

func TestMaintainer(t *testing.T) {
	mf := newMaintFixture(t)

	t.Run("LinkedIn 13 days out: one expiry warning to the brand's admins, once", func(t *testing.T) {
		_, c := mf.seed(t, core.ProviderLinkedIn, core.KindLinkedInOrg, mf.keys, "AQtoken", "", "", "")
		if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.connections SET token_expires_at = $2::timestamptz + interval '13 days' WHERE id = $1`, c, mf.now); err != nil {
			t.Fatal(err)
		}
		before := len(mf.emails(t, emailcore.KindConnectionExpiring))
		for range 2 {
			if err := mf.work(t, wsA(), c); err != nil {
				t.Fatal(err)
			}
		}
		got := mf.emails(t, emailcore.KindConnectionExpiring)
		if len(got) != before+1 || got[len(got)-1].To().Address != "a@example.in" || got[len(got)-1].Data()["days"] != "13" ||
			!strings.HasPrefix(got[len(got)-1].Data()["link"], "https://app.tuorps.test/") {
			t.Fatalf("expiry emails = %d (+%d)", len(got), len(got)-before)
		}
		if cc := mf.conn(t, c); cc.WarnedDays != 14 || cc.Status != core.StatusActive || cc.NextDueAt.IsZero() {
			t.Errorf("after warning: %+v", cc)
		}
	})

	t.Run("a token the platform rejects: needs re-auth, emailed and audited", func(t *testing.T) {
		mf.plat[core.ProviderMeta].check = func(string) (core.Health, error) { return core.Health{Valid: false}, nil }
		defer func() {
			mf.plat[core.ProviderMeta].check = func(string) (core.Health, error) { return core.Health{Valid: true}, nil }
		}()
		_, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
		before, audits := len(mf.emails(t, emailcore.KindConnectionNeedsReauth)), mf.audits(t, "connection.status")
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		if got := mf.plat[core.ProviderMeta].checked; got[len(got)-1] != "EAApage" {
			t.Errorf("checked with %q, want the Page token", got[len(got)-1])
		}
		cc := mf.conn(t, c)
		if cc.Status != core.StatusNeedsReauth || cc.LastError != core.ErrReauthRequired || !cc.NextDueAt.IsZero() {
			t.Errorf("after rejection: %+v", cc)
		}
		if len(mf.emails(t, emailcore.KindConnectionNeedsReauth)) != before+1 || mf.audits(t, "connection.status") != audits+1 {
			t.Error("no email or no audit row for the status change")
		}
	})

	t.Run("a platform outage is a transient error: recorded, retried by schedule, no email", func(t *testing.T) {
		mf.plat[core.ProviderMeta].check = func(string) (core.Health, error) { return core.Health{}, core.ErrProviderUnavailable }
		defer func() {
			mf.plat[core.ProviderMeta].check = func(string) (core.Health, error) { return core.Health{Valid: true}, nil }
		}()
		_, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
		before := len(mf.emails(t, emailcore.KindConnectionNeedsReauth))
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		cc := mf.conn(t, c)
		if cc.Status != core.StatusError || cc.LastError != core.ErrProviderUnavailable || cc.NextDueAt.IsZero() || cc.NextDueAt.After(mf.now.Add(day)) {
			t.Errorf("after outage: %+v", cc)
		}
		if len(mf.emails(t, emailcore.KindConnectionNeedsReauth)) != before {
			t.Error("an outage emailed the customer")
		}
	})

	t.Run("Instagram Login: refreshed when 40 days old, token resealed, expiry extended", func(t *testing.T) {
		mf.plat[core.ProviderInstagram].refresh = func(tok string) (core.TokenSet, error) {
			return core.BearerToken("IGrefreshed", 60*day), nil
		}
		g, c := mf.seed(t, core.ProviderInstagram, core.KindInstagramAccount, mf.keys, "IGlong", "", "", "")
		if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.oauth_grants SET token_issued_at = $2::timestamptz - interval '40 days' WHERE id = $1`, g, mf.now); err != nil {
			t.Fatal(err)
		}
		if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.connections SET token_expires_at = $2::timestamptz + interval '20 days' WHERE id = $1`, c, mf.now); err != nil {
			t.Fatal(err)
		}
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		if got := mf.plat[core.ProviderInstagram].refreshed; len(got) == 0 || got[len(got)-1] != "IGlong" {
			t.Fatalf("refreshed with %v", got)
		}
		var grant connections.Grant
		if err := mf.read(t, scope(wsA(), ownerA()), func(tx pgxTx) error {
			var err error
			grant, err = connections.GetGrant(t.Context(), tx, wsA(), brA1(), g)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if tok, err := mf.vault.Open(g, core.FieldAccessToken, grant.AccessTokenEnc); err != nil || tok != "IGrefreshed" || !grant.TokenIssuedAt.Equal(mf.now) {
			t.Errorf("grant after refresh: %q, %v, issued %s", tok, err, grant.TokenIssuedAt)
		}
		if cc := mf.conn(t, c); !cc.TokenExpiresAt.Equal(mf.now.Add(60 * day)) {
			t.Errorf("connection expiry = %s, want +60 days", cc.TokenExpiresAt)
		}
	})

	t.Run("Google: the check refreshes first and checks with the new access token", func(t *testing.T) {
		mf.plat[core.ProviderGoogle].refresh = func(tok string) (core.TokenSet, error) {
			if tok != "1//refresh" {
				return core.TokenSet{}, core.ErrReauthRequired
			}
			return core.BearerToken("ya29.fresh", time.Hour), nil
		}
		_, c := mf.seed(t, core.ProviderGoogle, core.KindYouTubeChannel, mf.keys, "ya29.stale", "1//refresh", "",
			`UPDATE tuorps.connections SET last_checked_at = NULL WHERE id = $1`)
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		if got := mf.plat[core.ProviderGoogle].checked; len(got) == 0 || got[len(got)-1] != "ya29.fresh" {
			t.Errorf("checked with %v", got)
		}
		if cc := mf.conn(t, c); cc.Status != core.StatusActive || !cc.LastCheckedAt.Equal(mf.now) {
			t.Errorf("after check: %+v", cc)
		}
	})

	t.Run("tokens sealed with an old key move to the active key", func(t *testing.T) {
		old := testKeyring(t, "k0")
		g, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, old, "EAAuser", "", "EAApage", "")
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.connections c JOIN tuorps.oauth_grants g ON g.id = c.grant_id
			WHERE c.id = $1 AND g.id = $2 AND c.token_key_id = 'k1' AND g.access_token_key_id = 'k1'`, c, g).Scan(&n)
		if n != 1 {
			t.Fatal("tokens still on the old key")
		}
		if tok, err := mf.vault.Open(c, core.FieldPageToken, mf.conn(t, c).TokenEnc); err != nil || tok != "EAApage" {
			t.Errorf("resealed page token = %q, %v", tok, err)
		}
	})

	t.Run("LinkedIn expired without a refresh token: needs re-auth", func(t *testing.T) {
		_, c := mf.seed(t, core.ProviderLinkedIn, core.KindLinkedInOrg, mf.keys, "AQtoken", "", "", "")
		if _, err := mf.admin.Exec(t.Context(), `UPDATE tuorps.connections SET token_expires_at = $2::timestamptz - interval '1 minute', warned_days = 3 WHERE id = $1`, c, mf.now); err != nil {
			t.Fatal(err)
		}
		if err := mf.work(t, wsA(), c); err != nil {
			t.Fatal(err)
		}
		if cc := mf.conn(t, c); cc.Status != core.StatusNeedsReauth || !cc.NextDueAt.IsZero() {
			t.Errorf("expired: %+v", cc)
		}
	})

	t.Run("a job naming another workspace touches nothing", func(t *testing.T) {
		_, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
		checks := len(mf.plat[core.ProviderMeta].checked)
		err := mf.work(t, wsB(), c)
		var cancel *river.JobCancelError
		if !errors.As(err, &cancel) || len(mf.plat[core.ProviderMeta].checked) != checks {
			t.Errorf("err = %v; platform calls %d -> %d", err, checks, len(mf.plat[core.ProviderMeta].checked))
		}
	})

}
