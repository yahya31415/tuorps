// Package core holds the pure rules for platform connections: providers and
// account kinds, authorize URLs, token-response parsing, the maintenance
// schedule, webhook signature checks and pinned API versions. It does no
// I/O and reads no clock (ADRs 0008-0010).
package core

import "errors"

var (
	ErrInvalidProvider = errors.New("connections: invalid provider")
	ErrInvalidKind     = errors.New("connections: invalid account kind")
)

// Provider is an OAuth provider: one login flow and one set of credentials.
type Provider string

const (
	ProviderMeta      Provider = "meta"      // Facebook Login for Business
	ProviderInstagram Provider = "instagram" // Instagram API with Instagram Login
	ProviderLinkedIn  Provider = "linkedin"
	ProviderGoogle    Provider = "google"
)

// ParseProvider validates a provider name.
func ParseProvider(s string) (Provider, error) {
	switch p := Provider(s); p {
	case ProviderMeta, ProviderInstagram, ProviderLinkedIn, ProviderGoogle:
		return p, nil
	}
	return "", ErrInvalidProvider
}

// Kind is the type of a connected account.
type Kind string

const (
	KindFacebookPage     Kind = "facebook_page"
	KindInstagramAccount Kind = "instagram_account"
	KindLinkedInOrg      Kind = "linkedin_org"
	KindYouTubeChannel   Kind = "youtube_channel"
	KindWhatsAppNumber   Kind = "whatsapp_number" // Embedded Signup, M6
)

// ParseKind validates an account kind.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindFacebookPage, KindInstagramAccount, KindLinkedInOrg, KindYouTubeChannel, KindWhatsAppNumber:
		return k, nil
	}
	return "", ErrInvalidKind
}

// Kinds lists the account kinds a login with p can connect. The result is
// a fresh slice.
func (p Provider) Kinds() []Kind {
	switch p {
	case ProviderMeta:
		return []Kind{KindFacebookPage, KindInstagramAccount}
	case ProviderInstagram:
		return []Kind{KindInstagramAccount}
	case ProviderLinkedIn:
		return []Kind{KindLinkedInOrg}
	case ProviderGoogle:
		return []Kind{KindYouTubeChannel}
	}
	return nil
}
