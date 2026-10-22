package core

// template is one kind's subject, plain-text body and HTML body fragment.
type template struct {
	subject string
	text    string
	html    string
}

const (
	textFooter = "\n--\nTuorps · Social media and WhatsApp for Indian businesses\nYou received this because of activity on your Tuorps account.\n"
	htmlHeader = `<!doctype html><html><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#1a1a1a">`
	htmlFooter = `<p style="color:#666;font-size:12px">Tuorps · Social media and WhatsApp for Indian businesses<br>You received this because of activity on your Tuorps account.</p></body></html>`
)

// templatesFor returns the templates for locale; English is the fallback
// and currently the only locale (Indic locales are V2).
func templatesFor(locale string) map[Kind]template {
	switch locale {
	default:
		return english()
	}
}

func english() map[Kind]template {
	return map[Kind]template{
		KindVerifyEmail: {
			subject: "Verify your email for Tuorps",
			text:    "Hi {{.name}},\n\nVerify your email address to finish setting up Tuorps:\n{{.link}}\n\nThe link expires in 24 hours. If you did not sign up, ignore this email.\n",
			html:    `<p>Hi {{.name}},</p><p>Verify your email address to finish setting up Tuorps:</p><p><a href="{{.link}}">Verify email</a></p><p>The link expires in 24 hours. If you did not sign up, ignore this email.</p>`,
		},
		KindPasswordReset: {
			subject: "Reset your Tuorps password",
			text:    "Hi {{.name}},\n\nUse this link to reset your password:\n{{.link}}\n\nThe link expires in 1 hour and works once. If you did not ask for this, ignore this email; your password is unchanged.\n",
			html:    `<p>Hi {{.name}},</p><p>Use this link to reset your password:</p><p><a href="{{.link}}">Reset password</a></p><p>The link expires in 1 hour and works once. If you did not ask for this, ignore this email; your password is unchanged.</p>`,
		},
		KindInvitation: {
			subject: "{{.inviter}} invited you to {{.workspace}} on Tuorps",
			text:    "{{.inviter}} invited you to join {{.workspace}} on Tuorps.\n\nAccept the invitation:\n{{.link}}\n\nThe invitation expires in 7 days.\n",
			html:    `<p>{{.inviter}} invited you to join <strong>{{.workspace}}</strong> on Tuorps.</p><p><a href="{{.link}}">Accept invitation</a></p><p>The invitation expires in 7 days.</p>`,
		},
		KindPasswordChanged: {
			subject: "Your Tuorps password was changed",
			text:    "Hi {{.name}},\n\nThe password for your Tuorps account was just changed, and other sessions were signed out.\n\nIf this was not you, reset your password immediately and contact support.\n",
			html:    `<p>Hi {{.name}},</p><p>The password for your Tuorps account was just changed, and other sessions were signed out.</p><p>If this was not you, reset your password immediately and contact support.</p>`,
		},
		KindMFAEnabled: {
			subject: "Two-factor authentication is on",
			text:    "Hi {{.name}},\n\nTwo-factor authentication was turned on for your Tuorps account. Keep your recovery codes somewhere safe.\n\nIf this was not you, reset your password immediately.\n",
			html:    `<p>Hi {{.name}},</p><p>Two-factor authentication was turned on for your Tuorps account. Keep your recovery codes somewhere safe.</p><p>If this was not you, reset your password immediately.</p>`,
		},
		KindMFADisabled: {
			subject: "Two-factor authentication was turned off",
			text:    "Hi {{.name}},\n\nTwo-factor authentication was turned off for your Tuorps account.\n\nIf this was not you, reset your password and turn two-factor authentication back on.\n",
			html:    `<p>Hi {{.name}},</p><p>Two-factor authentication was turned off for your Tuorps account.</p><p>If this was not you, reset your password and turn two-factor authentication back on.</p>`,
		},
		KindConnectionExpiring: {
			subject: "Your {{.platform}} connection {{.account}} stops working in {{.days}} days",
			text:    "Hi {{.name}},\n\nThe {{.platform}} account {{.account}} connected to Tuorps stops working in {{.days}} days, when its login expires. Reconnect it to keep publishing and receiving messages:\n{{.link}}\n",
			html:    `<p>Hi {{.name}},</p><p>The {{.platform}} account <strong>{{.account}}</strong> connected to Tuorps stops working in {{.days}} days, when its login expires. Reconnect it to keep publishing and receiving messages:</p><p><a href="{{.link}}">Reconnect {{.account}}</a></p>`,
		},
		KindConnectionNeedsReauth: {
			subject: "Reconnect {{.account}} on {{.platform}}",
			text:    "Hi {{.name}},\n\nTuorps can no longer reach the {{.platform}} account {{.account}}: its login expired or was removed. Reconnect it to resume publishing and messages:\n{{.link}}\n",
			html:    `<p>Hi {{.name}},</p><p>Tuorps can no longer reach the {{.platform}} account <strong>{{.account}}</strong>: its login expired or was removed. Reconnect it to resume publishing and messages:</p><p><a href="{{.link}}">Reconnect {{.account}}</a></p>`,
		},
		KindVersionSunset: {
			subject: "Platform API {{.api}} {{.version}} sunsets in {{.days}} days",
			text:    "The pinned {{.api}} API version {{.version}} sunsets on {{.sunset}}, in {{.days}} days. Move to a newer version and update docs/api-reference.md before then.\n",
			html:    `<p>The pinned <strong>{{.api}}</strong> API version {{.version}} sunsets on {{.sunset}}, in {{.days}} days. Move to a newer version and update docs/api-reference.md before then.</p>`,
		},
		KindAccountExists: {
			subject: "Someone tried to sign up with your email",
			text:    "Hi {{.name}},\n\nSomeone tried to create a Tuorps account with this email address, but you already have one. If it was you, sign in or reset your password:\n{{.link}}\n\nIf it was not you, no action is needed.\n",
			html:    `<p>Hi {{.name}},</p><p>Someone tried to create a Tuorps account with this email address, but you already have one. If it was you, sign in or reset your password:</p><p><a href="{{.link}}">Reset password</a></p><p>If it was not you, no action is needed.</p>`,
		},
	}
}
