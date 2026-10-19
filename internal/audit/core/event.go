// Package core defines audit records as validated values. Constructors
// refuse malformed events and details that name secrets, so a record that
// could leak a password or token cannot be built at all.
package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"
)

const (
	maxAction     = 64
	maxTargetType = 32
	maxTargetID   = 64
	maxDetails    = 16
	maxDetailKey  = 32
	maxDetailVal  = 256
	maxRequestID  = 64
)

var (
	ErrInvalidEvent    = errors.New("audit: invalid event")
	ErrSensitiveDetail = errors.New("audit: detail key names a secret")
	ErrInvalidRecord   = errors.New("audit: invalid record")
)

// sensitiveMarkers are substrings that may not appear in a detail key.
func sensitiveMarkers() []string {
	return []string{"password", "passwd", "token", "secret", "otp", "code", "pin"}
}

// Event says what happened, independent of who did it and when.
type Event struct {
	action     string
	targetType string
	targetID   string
	details    map[string]string
}

// NewEvent validates and returns an event. action is dotted lowercase
// ("brand.create"), targetType a lowercase word, details short non-secret
// key/value pairs.
func NewEvent(action, targetType, targetID string, details map[string]string) (Event, error) {
	if len(action) > maxAction || !dottedName(action) {
		return Event{}, ErrInvalidEvent
	}
	if len(targetType) > maxTargetType || !lowerWord(targetType) || len(targetID) > maxTargetID {
		return Event{}, ErrInvalidEvent
	}
	if len(details) > maxDetails {
		return Event{}, ErrInvalidEvent
	}
	for k, v := range details {
		if len(k) > maxDetailKey || !lowerWord(k) || len(v) > maxDetailVal || !utf8.ValidString(v) {
			return Event{}, ErrInvalidEvent
		}
		for _, m := range sensitiveMarkers() {
			if strings.Contains(k, m) {
				return Event{}, ErrSensitiveDetail
			}
		}
	}
	return Event{action: action, targetType: targetType, targetID: targetID, details: maps.Clone(details)}, nil
}

func (e Event) Action() string     { return e.action }
func (e Event) TargetType() string { return e.targetType }
func (e Event) TargetID() string   { return e.targetID }

// Details returns a copy of the event's details.
func (e Event) Details() map[string]string { return maps.Clone(e.details) }

// Meta is request context recorded with an event.
type Meta struct {
	RequestID string
	IPHash    string // see HashIP; never a raw address
}

// Record is one audit log row.
type Record struct {
	ID          uuid.UUID
	WorkspaceID *uuid.UUID // nil for account-level events
	ActorUserID *uuid.UUID // nil for system events
	Action      string
	TargetType  string
	TargetID    string
	RequestID   string
	IPHash      string
	Details     map[string]string
	At          time.Time
}

// NewRecord builds a record. workspace and actor may each be uuid.Nil, but
// not both: account-level events need an actor, system events a workspace.
func NewRecord(id, workspace, actor uuid.UUID, ev Event, meta Meta, at time.Time) (Record, error) {
	if id == uuid.Nil() || at.IsZero() || ev.action == "" || (workspace == uuid.Nil() && actor == uuid.Nil()) {
		return Record{}, ErrInvalidRecord
	}
	r := Record{
		ID:         id,
		Action:     ev.action,
		TargetType: ev.targetType,
		TargetID:   ev.targetID,
		RequestID:  truncate(meta.RequestID, maxRequestID),
		IPHash:     truncate(meta.IPHash, maxRequestID),
		Details:    ev.Details(),
		At:         at,
	}
	if workspace != uuid.Nil() {
		r.WorkspaceID = &workspace
	}
	if actor != uuid.Nil() {
		r.ActorUserID = &actor
	}
	return r, nil
}

// HashIP keys a client address with HMAC-SHA256 so the log can correlate
// requests from one address without storing it. IPv4-mapped IPv6 addresses
// hash as IPv4. An invalid address hashes to "".
func HashIP(key []byte, addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	b := addr.Unmap().AsSlice()
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// lowerWord reports whether s is [a-z][a-z0-9_]*.
func lowerWord(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// dottedName reports whether s is two or more lowerWords joined by dots.
func dottedName(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !lowerWord(p) {
			return false
		}
	}
	return true
}
