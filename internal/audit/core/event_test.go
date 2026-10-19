package core_test

import (
	"errors"
	"maps"
	"net/netip"
	"strings"
	"testing"
	"testing/quick"
	"time"
	"uuid"

	"github.com/yahya31415/tuorps/internal/audit/core"
)

func wsID() uuid.UUID    { return uuid.MustParse("0199c3f0-0000-7000-8000-00000000000a") }
func actorID() uuid.UUID { return uuid.MustParse("0199c3f0-0000-7000-8000-0000000000a1") }
func recordID() uuid.UUID {
	return uuid.MustParse("0199c3f0-0000-7000-8000-0000000003a1")
}
func when() time.Time { return time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) }

func TestNewEventAcceptsWellFormedEvents(t *testing.T) {
	details := map[string]string{"name": "Diwali Sale", "role": "publisher"}
	ev, err := core.NewEvent("brand.create", "brand", "0199c3f0-0000-7000-8000-00000000ba01", details)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if ev.Action() != "brand.create" || ev.TargetType() != "brand" || ev.TargetID() != "0199c3f0-0000-7000-8000-00000000ba01" {
		t.Errorf("event = %+v", ev)
	}
	details["name"] = "mutated"
	if ev.Details()["name"] != "Diwali Sale" {
		t.Error("event shares the caller's details map")
	}
	got := ev.Details()
	got["role"] = "owner"
	if ev.Details()["role"] != "publisher" {
		t.Error("Details exposes internal state")
	}
}

func TestNewEventRejects(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct {
		name                       string
		action, targetType, target string
		details                    map[string]string
		want                       error
	}{
		{"action without a dot", "create", "brand", "x", nil, core.ErrInvalidEvent},
		{"action with uppercase", "Brand.Create", "brand", "x", nil, core.ErrInvalidEvent},
		{"action with trailing dot", "brand.", "brand", "x", nil, core.ErrInvalidEvent},
		{"action too long", "a." + strings.Repeat("b", 70), "brand", "x", nil, core.ErrInvalidEvent},
		{"empty target type", "brand.create", "", "x", nil, core.ErrInvalidEvent},
		{"target type with dot", "brand.create", "brand.x", "x", nil, core.ErrInvalidEvent},
		{"target id too long", "brand.create", "brand", long, nil, core.ErrInvalidEvent},
		{"bad detail key", "brand.create", "brand", "x", map[string]string{"Bad Key": "v"}, core.ErrInvalidEvent},
		{"detail value too long", "brand.create", "brand", "x", map[string]string{"name": long}, core.ErrInvalidEvent},
		{"detail value not utf-8", "brand.create", "brand", "x", map[string]string{"name": "\xff"}, core.ErrInvalidEvent},
		{"too many details", "brand.create", "brand", "x", manyDetails(17), core.ErrInvalidEvent},
		{"password detail", "auth.password_change", "user", "x", map[string]string{"new_password": "hunter2"}, core.ErrSensitiveDetail},
		{"token detail", "auth.reset", "user", "x", map[string]string{"reset_token": "abc"}, core.ErrSensitiveDetail},
		{"otp detail", "auth.mfa", "user", "x", map[string]string{"otp": "123456"}, core.ErrSensitiveDetail},
		{"recovery code detail", "auth.mfa", "user", "x", map[string]string{"recovery_code": "x"}, core.ErrSensitiveDetail},
		{"secret detail", "auth.mfa", "user", "x", map[string]string{"totp_secret": "x"}, core.ErrSensitiveDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := core.NewEvent(tc.action, tc.targetType, tc.target, tc.details)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error echoes a detail value: %v", err)
			}
		})
	}
}

func manyDetails(n int) map[string]string {
	m := map[string]string{}
	for i := range n {
		m["k"+strings.Repeat("x", i)] = "v"
	}
	return m
}

func TestNewRecord(t *testing.T) {
	ev, _ := core.NewEvent("brand.create", "brand", "b1", map[string]string{"name": "x"})
	meta := core.Meta{RequestID: "0199c3f0-7a2b-7c3d-8e4f-5a6b7c8d9e0f", IPHash: "abc"}

	t.Run("workspace event", func(t *testing.T) {
		r, err := core.NewRecord(recordID(), wsID(), actorID(), ev, meta, when())
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != recordID() || *r.WorkspaceID != wsID() || *r.ActorUserID != actorID() || r.Action != "brand.create" ||
			r.TargetType != "brand" || r.TargetID != "b1" || r.RequestID != meta.RequestID || r.IPHash != "abc" ||
			!r.At.Equal(when()) || !maps.Equal(r.Details, map[string]string{"name": "x"}) {
			t.Errorf("record = %+v", r)
		}
	})
	t.Run("account-level event has no workspace", func(t *testing.T) {
		r, err := core.NewRecord(recordID(), uuid.Nil(), actorID(), ev, meta, when())
		if err != nil || r.WorkspaceID != nil || *r.ActorUserID != actorID() {
			t.Fatalf("record = %+v, %v", r, err)
		}
	})
	t.Run("system event in a workspace has no actorID()", func(t *testing.T) {
		r, err := core.NewRecord(recordID(), wsID(), uuid.Nil(), ev, meta, when())
		if err != nil || r.ActorUserID != nil {
			t.Fatalf("record = %+v, %v", r, err)
		}
	})
	t.Run("needs a workspace or an actorID()", func(t *testing.T) {
		if _, err := core.NewRecord(recordID(), uuid.Nil(), uuid.Nil(), ev, meta, when()); !errors.Is(err, core.ErrInvalidRecord) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("needs an ID, a time and an event", func(t *testing.T) {
		if _, err := core.NewRecord(uuid.Nil(), wsID(), actorID(), ev, meta, when()); !errors.Is(err, core.ErrInvalidRecord) {
			t.Errorf("nil id: %v", err)
		}
		if _, err := core.NewRecord(recordID(), wsID(), actorID(), ev, meta, time.Time{}); !errors.Is(err, core.ErrInvalidRecord) {
			t.Errorf("zero time: %v", err)
		}
		if _, err := core.NewRecord(recordID(), wsID(), actorID(), core.Event{}, meta, when()); !errors.Is(err, core.ErrInvalidRecord) {
			t.Errorf("zero event: %v", err)
		}
	})
	t.Run("oversized request ID is truncated, not rejected", func(t *testing.T) {
		r, err := core.NewRecord(recordID(), wsID(), actorID(), ev, core.Meta{RequestID: strings.Repeat("r", 100)}, when())
		if err != nil || len(r.RequestID) != 64 {
			t.Fatalf("len = %d, %v", len(r.RequestID), err)
		}
	})
}

func TestHashIP(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	a := core.HashIP(key, netip.MustParseAddr("203.0.113.7"))
	if len(a) != 32 || strings.Contains(a, "203") {
		t.Fatalf("HashIP = %q", a)
	}
	if a != core.HashIP(key, netip.MustParseAddr("203.0.113.7")) {
		t.Error("not deterministic")
	}
	if a == core.HashIP(key, netip.MustParseAddr("203.0.113.8")) {
		t.Error("different addresses collide")
	}
	if a == core.HashIP([]byte("another key, another key, another"), netip.MustParseAddr("203.0.113.7")) {
		t.Error("hash ignores the key")
	}
	if core.HashIP(key, netip.Addr{}) != "" {
		t.Error("invalid address should hash to empty")
	}
	if core.HashIP(key, netip.MustParseAddr("::ffff:203.0.113.7")) != a {
		t.Error("IPv4-mapped IPv6 hashes differently from IPv4")
	}
}

// Property: no accepted event carries a detail whose key names a secret.
func TestPropertyAcceptedDetailsNeverNameSecrets(t *testing.T) {
	prop := func(prefix, suffix uint8, secretIdx uint8, value string) bool {
		secrets := []string{"password", "token", "secret", "otp", "totp", "code", "pin"}
		letters := "abcdefghijklmnopqrstuvwxyz"
		key := letters[:prefix%5] + secrets[int(secretIdx)%len(secrets)] + letters[:suffix%5]
		_, err := core.NewEvent("x.y", "t", "id", map[string]string{key: value})
		return err != nil
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Error(err)
	}
}

func FuzzNewEvent(f *testing.F) {
	f.Add("brand.create", "brand", "id", "name", "v")
	f.Add("a.b", "t", "", "pass_word", "x")
	f.Fuzz(func(t *testing.T, action, targetType, target, k, v string) {
		ev, err := core.NewEvent(action, targetType, target, map[string]string{k: v})
		if err != nil {
			return
		}
		if len(ev.Action()) > 64 || len(ev.TargetType()) > 32 || len(ev.TargetID()) > 64 || !strings.Contains(ev.Action(), ".") {
			t.Fatalf("accepted out-of-bounds event %+v", ev)
		}
		for dk, dv := range ev.Details() {
			if len(dk) > 32 || len(dv) > 256 {
				t.Fatalf("accepted out-of-bounds detail %q=%q", dk, dv)
			}
		}
	})
}
