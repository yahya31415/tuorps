package core_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

func TestParseProvider(t *testing.T) {
	for _, s := range []string{"meta", "instagram", "linkedin", "google"} {
		if p, err := core.ParseProvider(s); err != nil || string(p) != s {
			t.Errorf("ParseProvider(%q) = %q, %v", s, p, err)
		}
	}
	for _, s := range []string{"", "Meta", "facebook", "youtube", "whatsapp", "meta "} {
		if _, err := core.ParseProvider(s); !errors.Is(err, core.ErrInvalidProvider) {
			t.Errorf("ParseProvider(%q) err = %v, want ErrInvalidProvider", s, err)
		}
	}
}

func TestParseKind(t *testing.T) {
	for _, s := range []string{"facebook_page", "instagram_account", "linkedin_org", "youtube_channel", "whatsapp_number"} {
		if k, err := core.ParseKind(s); err != nil || string(k) != s {
			t.Errorf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	for _, s := range []string{"", "page", "Facebook_Page", "youtube"} {
		if _, err := core.ParseKind(s); !errors.Is(err, core.ErrInvalidKind) {
			t.Errorf("ParseKind(%q) err = %v, want ErrInvalidKind", s, err)
		}
	}
}

// A login only yields the account kinds its platform exposes (ADR 0009).
// WhatsApp numbers are a valid kind but come from Embedded Signup (M6),
// not from any M3 login.
func TestProviderKinds(t *testing.T) {
	cases := map[core.Provider][]core.Kind{
		core.ProviderMeta:      {core.KindFacebookPage, core.KindInstagramAccount},
		core.ProviderInstagram: {core.KindInstagramAccount},
		core.ProviderLinkedIn:  {core.KindLinkedInOrg},
		core.ProviderGoogle:    {core.KindYouTubeChannel},
	}
	for p, want := range cases {
		got := p.Kinds()
		if !slices.Equal(got, want) {
			t.Errorf("%s.Kinds() = %v, want %v", p, got, want)
		}
		got[0] = core.KindWhatsAppNumber
		if again := p.Kinds(); !slices.Equal(again, want) {
			t.Errorf("%s.Kinds() shares its slice with callers", p)
		}
	}
}
