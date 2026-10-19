package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive configuration value. Every rendering path
// (fmt, slog, JSON) prints a placeholder; only Reveal returns the value.
type Secret struct{ value string }

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{value: v} }

// Reveal returns the underlying value. Call it only where the value is used.
func (s Secret) Reveal() string { return s.value }

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
