// Package webhook provides a forge-agnostic HTTP webhook abstraction that can
// be reused for GitHub, GitLab, Bitbucket, and other forge webhook receivers.
// Platform-specific parsing and verification sit above this layer.
package webhook

import "time"

// ForgeWebhookEvent is the normalised shape passed through the EventDispatcher.
// Platform handlers fill this after verifying the request signature and
// deduplicating the delivery ID.
type ForgeWebhookEvent struct {
	// Source identifies the forge platform, e.g. "gitlab".
	Source string `json:"source"`
	// EventType is the IPC event name, e.g. "gitlab.pipeline".
	EventType string `json:"event_type"`
	// DeliveryID is the unique delivery identifier for idempotency tracking.
	DeliveryID string `json:"delivery_id"`
	// OccurredAt is the canonical timestamp of the underlying action.
	OccurredAt time.Time `json:"occurred_at"`
	// Payload holds the platform-specific parsed event body.
	Payload interface{} `json:"payload"`
	// ProjectID is the string representation of the project / repository ID.
	ProjectID string `json:"project_id,omitempty"`
	// ProjectURL is the web URL of the project.
	ProjectURL string `json:"project_url,omitempty"`
}
