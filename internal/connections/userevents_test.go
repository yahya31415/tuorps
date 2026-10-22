package connections_test

import (
	"testing"

	"github.com/yahya31415/tuorps/internal/connections"
	"github.com/yahya31415/tuorps/internal/connections/core"
	emailcore "github.com/yahya31415/tuorps/internal/email/core"
)

func TestDeauthorizeUser(t *testing.T) {
	mf := newMaintFixture(t)
	g, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
	_, other := mf.seed(t, core.ProviderLinkedIn, core.KindLinkedInOrg, mf.keys, "AQtoken", "", "", "")
	before := len(mf.emails(t, emailcore.KindConnectionNeedsReauth))

	if err := connections.DeauthorizeUser(t.Context(), mf.deps, core.ProviderMeta, "u-meta"); err != nil {
		t.Fatal(err)
	}
	cc := mf.conn(t, c)
	if cc.Status != core.StatusNeedsReauth || cc.LastError != core.ErrReauthRequired || cc.TokenEnc != "" || !cc.NextDueAt.IsZero() {
		t.Errorf("connection after deauthorize = %+v", cc)
	}
	var wiped int
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND access_token_enc IS NULL`, g).Scan(&wiped)
	if wiped != 1 || len(mf.emails(t, emailcore.KindConnectionNeedsReauth)) != before+1 || mf.audits(t, "connection.deauthorized") != 1 {
		t.Errorf("wiped %d, emails +%d, audits %d", wiped, len(mf.emails(t, emailcore.KindConnectionNeedsReauth))-before, mf.audits(t, "connection.deauthorized"))
	}
	if mf.conn(t, other).Status != core.StatusActive {
		t.Error("another platform's connection was touched")
	}
	// An unknown user is not an error (the platform may know users we never stored).
	if err := connections.DeauthorizeUser(t.Context(), mf.deps, core.ProviderMeta, "nobody"); err != nil {
		t.Errorf("unknown user: %v", err)
	}
}

// Instagram does not document whether callbacks carry the Instagram-scoped
// id (stored on the grant) or the account id (the connection's external
// id), so both are matched (ADR 0010).
func TestDeauthorizeInstagramMatchesTheAccountID(t *testing.T) {
	mf := newMaintFixture(t)
	_, c := mf.seed(t, core.ProviderInstagram, core.KindInstagramAccount, mf.keys, "IGlong", "", "", "")
	if err := connections.DeauthorizeUser(t.Context(), mf.deps, core.ProviderInstagram, mf.conn(t, c).ExternalID); err != nil {
		t.Fatal(err)
	}
	if mf.conn(t, c).Status != core.StatusNeedsReauth {
		t.Error("matching by the account id did not deauthorize")
	}
}

func TestDeleteUserData(t *testing.T) {
	mf := newMaintFixture(t)
	g, c := mf.seed(t, core.ProviderMeta, core.KindFacebookPage, mf.keys, "EAAuser", "", "EAApage", "")
	if _, err := mf.admin.Exec(t.Context(), `INSERT INTO tuorps.data_deletion_requests (confirmation_code, provider, provider_user_id, status, requested_at) VALUES ('ddr-code-1', 'meta', 'u-meta', 'pending', now())`); err != nil {
		t.Fatal(err)
	}
	if err := connections.DeleteUserData(t.Context(), mf.deps, core.ProviderMeta, "u-meta", "ddr-code-1"); err != nil {
		t.Fatal(err)
	}
	cc := mf.conn(t, c)
	var wiped int
	var status string
	_ = mf.admin.QueryRow(t.Context(), `SELECT count(*) FROM tuorps.oauth_grants WHERE id = $1 AND access_token_enc IS NULL AND refresh_token_enc IS NULL`, g).Scan(&wiped)
	_ = mf.admin.QueryRow(t.Context(), `SELECT status FROM tuorps.data_deletion_requests WHERE confirmation_code = 'ddr-code-1'`).Scan(&status)
	if cc.Status != core.StatusDisconnected || cc.TokenEnc != "" || wiped != 1 || status != "done" || mf.audits(t, "connection.data_deletion") != 1 {
		t.Errorf("after deletion: connection %s, wiped %d, request %s, audits %d", cc.Status, wiped, status, mf.audits(t, "connection.data_deletion"))
	}
}
