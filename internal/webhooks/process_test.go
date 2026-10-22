package webhooks_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
	"github.com/yahya31415/tuorps/internal/webhooks"
)

func keys(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New(map[string][]byte{"k1": bytes.Repeat([]byte{3}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func job(t *testing.T, k *keyring.Keyring, p core.Provider, e webhooks.Event, payload []byte) *river.Job[webhooks.ProcessArgs] {
	t.Helper()
	s, err := k.Seal(rand.Reader, payload, []byte(webhooks.ProcessAAD))
	if err != nil {
		t.Fatal(err)
	}
	return &river.Job[webhooks.ProcessArgs]{Args: webhooks.ProcessArgs{Provider: string(p), Event: e, Sealed: s.Encode()}}
}

type call struct{ provider, user, code string }

func TestProcessor(t *testing.T) {
	k := keys(t)
	var deauth, deleted []call
	p := webhooks.NewProcessor(webhooks.ProcessorDeps{
		Keys: k,
		Deauthorize: func(_ context.Context, pr core.Provider, user string) error {
			deauth = append(deauth, call{string(pr), user, ""})
			return nil
		},
		DeleteData: func(_ context.Context, pr core.Provider, user, code string) error {
			deleted = append(deleted, call{string(pr), user, code})
			return nil
		},
	})

	if err := p.Work(t.Context(), job(t, k, core.ProviderMeta, webhooks.EventDeauthorize, []byte("10228374655566"))); err != nil || len(deauth) != 1 || deauth[0] != (call{"meta", "10228374655566", ""}) {
		t.Errorf("deauthorize: %v, calls %v", err, deauth)
	}
	payload, _ := json.Marshal(webhooks.Deletion{ProviderUserID: "17841400000000009", ConfirmationCode: "code-1"})
	if err := p.Work(t.Context(), job(t, k, core.ProviderInstagram, webhooks.EventDataDeletion, payload)); err != nil || len(deleted) != 1 || deleted[0] != (call{"instagram", "17841400000000009", "code-1"}) {
		t.Errorf("data deletion: %v, calls %v", err, deleted)
	}
	// Notifications are acknowledged; the inbox (M5) will consume them.
	if err := p.Work(t.Context(), job(t, k, core.ProviderLinkedIn, webhooks.EventNotification, []byte(`{"type":"x"}`))); err != nil {
		t.Errorf("notification: %v", err)
	}

	cancelled := func(err error) bool {
		var c *river.JobCancelError
		return errors.As(err, &c)
	}
	bad := job(t, k, core.ProviderMeta, webhooks.EventDeauthorize, []byte("u"))
	bad.Args.Sealed = "v1.k1.garbage"
	if err := p.Work(t.Context(), bad); !cancelled(err) {
		t.Errorf("undecryptable payload: %v, want cancel", err)
	}
	if err := p.Work(t.Context(), job(t, k, core.ProviderMeta, "mystery", []byte("x"))); !cancelled(err) {
		t.Errorf("unknown event: %v, want cancel", err)
	}
	if err := p.Work(t.Context(), job(t, k, "myspace", webhooks.EventDeauthorize, []byte("x"))); !cancelled(err) {
		t.Errorf("unknown provider: %v, want cancel", err)
	}
}

func TestProcessorRetriesHandlerFailures(t *testing.T) {
	k := keys(t)
	boom := errors.New("database unavailable")
	p := webhooks.NewProcessor(webhooks.ProcessorDeps{Keys: k, Deauthorize: func(context.Context, core.Provider, string) error { return boom }})
	if err := p.Work(t.Context(), job(t, k, core.ProviderMeta, webhooks.EventDeauthorize, []byte("u"))); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the handler error returned for a retry", err)
	}
}
