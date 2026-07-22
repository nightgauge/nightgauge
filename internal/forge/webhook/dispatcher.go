package webhook

import "context"

// EmitFunc is the signature of ipc.Server.Emit. The dispatcher takes a
// function value rather than a *ipc.Server pointer so this package never
// imports internal/ipc — dependency direction flows one-way through cmd/.
type EmitFunc func(event string, data interface{})

// EventDispatcher receives verified, deduplicated forge webhook events.
// Implementations must be goroutine-safe — the HTTP server may invoke
// Dispatch concurrently from multiple request goroutines.
type EventDispatcher interface {
	Dispatch(ctx context.Context, event ForgeWebhookEvent) error
}

// NewIPCEventDispatcher returns an EventDispatcher that publishes each event
// onto the IPC bus via emit. The IPC event name is taken from
// event.EventType; the payload is the full ForgeWebhookEvent.
func NewIPCEventDispatcher(emit EmitFunc) EventDispatcher {
	return &ipcEventDispatcher{emit: emit}
}

type ipcEventDispatcher struct {
	emit EmitFunc
}

func (d *ipcEventDispatcher) Dispatch(_ context.Context, event ForgeWebhookEvent) error {
	if d.emit == nil {
		return nil
	}
	d.emit(event.EventType, event)
	return nil
}
