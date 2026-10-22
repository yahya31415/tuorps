package webhooks

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/yahya31415/tuorps/internal/connections/core"
	"github.com/yahya31415/tuorps/internal/platform/keyring"
)

var errPayload = errors.New("webhooks: unusable payload")

// ProcessorDeps are the handlers a delivery is dispatched to.
type ProcessorDeps struct {
	Keys        *keyring.Keyring
	Deauthorize func(ctx context.Context, p core.Provider, providerUserID string) error
	DeleteData  func(ctx context.Context, p core.Provider, providerUserID, confirmationCode string) error
}

// Processor works webhooks.process jobs: it opens the sealed payload and
// dispatches it. Notifications are acknowledged here; the inbox (M5) will
// consume them. Payloads that cannot be opened or understood are cancelled,
// not retried; handler failures are returned so River retries them.
type Processor struct {
	river.WorkerDefaults[ProcessArgs]
	ProcessorDeps
}

// NewProcessor returns a Processor.
func NewProcessor(d ProcessorDeps) *Processor { return &Processor{ProcessorDeps: d} }

// Timeout bounds one delivery's handling.
func (p *Processor) Timeout(*river.Job[ProcessArgs]) time.Duration { return time.Minute }

func (p *Processor) Work(ctx context.Context, j *river.Job[ProcessArgs]) error {
	provider, err := core.ParseProvider(j.Args.Provider)
	if err != nil {
		return river.JobCancel(errPayload)
	}
	sealed, err := keyring.Decode(j.Args.Sealed)
	if err != nil {
		return river.JobCancel(errPayload)
	}
	payload, err := p.Keys.Open(sealed, []byte(ProcessAAD))
	if err != nil {
		return river.JobCancel(errPayload)
	}
	switch j.Args.Event {
	case EventNotification:
		return nil
	case EventDeauthorize:
		if len(payload) == 0 || p.Deauthorize == nil {
			return river.JobCancel(errPayload)
		}
		return wrap("deauthorize", p.Deauthorize(ctx, provider, string(payload)))
	case EventDataDeletion:
		var d Deletion
		if json.Unmarshal(payload, &d) != nil || d.ProviderUserID == "" || d.ConfirmationCode == "" || p.DeleteData == nil {
			return river.JobCancel(errPayload)
		}
		return wrap("data deletion", p.DeleteData(ctx, provider, d.ProviderUserID, d.ConfirmationCode))
	}
	return river.JobCancel(errPayload)
}

func wrap(op string, err error) error {
	if err != nil {
		return fmt.Errorf("webhooks: %s: %w", op, err)
	}
	return nil
}
