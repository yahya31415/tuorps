package core

import (
	"errors"
	"net/url"
	"slices"
	"strings"
)

// ErrInvalidOAuthClient means the provider credentials, redirect URI or
// state cannot form an authorize URL.
var ErrInvalidOAuthClient = errors.New("connections: invalid OAuth client configuration")

// OAuthClient is the public part of a provider's app registration.
type OAuthClient struct {
	ClientID    string
	RedirectURI string // absolute http(s), no fragment; registered exactly
	ConfigID    string // Meta: Facebook Login for Business configuration
}

const googleScopePrefix = "https://www.googleapis.com/auth/"

// RequestedScopes lists the scopes Tuorps asks p for. Meta's permissions
// live in the login configuration (config_id), so it requests none here.
// The result is a fresh slice.
func RequestedScopes(p Provider) []string {
	switch p {
	case ProviderInstagram:
		return []string{"instagram_business_basic", "instagram_business_content_publish", "instagram_business_manage_comments", "instagram_business_manage_messages", "instagram_business_manage_insights"}
	case ProviderLinkedIn:
		return []string{"w_organization_social", "r_organization_social", "rw_organization_admin", "r_organization_admin"}
	case ProviderGoogle:
		return []string{googleScopePrefix + "youtube.upload", googleScopePrefix + "youtube.force-ssl", googleScopePrefix + "youtube.readonly", googleScopePrefix + "yt-analytics.readonly"}
	}
	return nil
}

// AuthorizeURL builds the URL that sends the user to p's consent screen
// (docs/api-reference.md, "OAuth, tokens and webhook security"). No
// provider documents PKCE for web clients, so state is the CSRF defence
// (ADR 0008).
func AuthorizeURL(p Provider, c OAuthClient, state string) (string, error) {
	if _, err := ParseProvider(string(p)); err != nil {
		return "", err
	}
	if c.ClientID == "" || state == "" || !validRedirect(c.RedirectURI) || (p == ProviderMeta && c.ConfigID == "") {
		return "", ErrInvalidOAuthClient
	}
	q := url.Values{
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	var endpoint string
	switch p {
	case ProviderMeta:
		endpoint = "https://www.facebook.com/" + GraphVersion + "/dialog/oauth"
		q.Set("config_id", c.ConfigID)
	case ProviderInstagram:
		endpoint = "https://www.instagram.com/oauth/authorize"
		q.Set("scope", strings.Join(RequestedScopes(p), ","))
	case ProviderLinkedIn:
		endpoint = "https://www.linkedin.com/oauth/v2/authorization"
		q.Set("scope", strings.Join(RequestedScopes(p), " "))
	case ProviderGoogle:
		endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
		q.Set("scope", strings.Join(RequestedScopes(p), " "))
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
		q.Set("include_granted_scopes", "true")
	}
	// Encode escapes a literal '+' as %2B, so every '+' left is a space;
	// LinkedIn documents spaces as %20.
	return endpoint + "?" + strings.ReplaceAll(q.Encode(), "+", "%20"), nil
}

func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && slices.Contains([]string{"https", "http"}, u.Scheme) && u.Host != "" &&
		!strings.Contains(raw, "#") && u.User == nil
}
