// Package notify defines the Notifier interface and its implementations.
// Each implementation dispatches an AlertEvent to one notification backend
// (email, Slack webhook, generic webhook, etc.).
package notify

import (
	"context"

	"github.com/myrrolinz/cronmon/internal/model"
)

// Notifier is the interface implemented by all notification backends.
// One Notifier handles exactly one channel type (e.g. "email").
type Notifier interface {
	// Send delivers the alert described by event.
	// Implementations must respect ctx cancellation / deadline for any
	// network I/O they perform.
	Send(ctx context.Context, event model.AlertEvent) error

	// Type returns the channel type string this notifier handles,
	// matching model.Channel.Type (e.g. "email", "slack", "webhook").
	Type() string
}
