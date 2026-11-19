package alert

import (
	"context"
	"errors"
	"time"

	"github.com/retnd/retnd/core/internal/obs"
)

const (
	DialectGeneric      = "generic"
	DialectAlertmanager = "alertmanager"
)

// HTTPSink is a placeholder: the red tests land first.
type HTTPSink struct{}

func NewHTTPSink(obs.Secret, string, time.Duration, time.Duration, bool) (*HTTPSink, error) {
	return nil, errors.New("not implemented")
}

func (*HTTPSink) Deliver(context.Context, Alert) error { return errors.New("not implemented") }

func (d *Dispatcher) Refresh(context.Context, time.Time) {}
func (d *Dispatcher) AdoptState(*Dispatcher)             {}
func (d *Dispatcher) FiringSnapshot() map[string]bool    { return nil }
