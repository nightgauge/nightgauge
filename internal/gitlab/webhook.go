package gitlab

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GitLab X-Gitlab-Event header values for supported project-level hooks.
const (
	EventKindPipeline     = "Pipeline Hook"
	EventKindMergeRequest = "Merge Request Hook"
	EventKindNote         = "Note Hook"
	EventKindPush         = "Push Hook"
)

// ErrUnsupportedEventKind is returned by ParseWebhookPayload when the
// X-Gitlab-Event value is not in the supported set. Callers should log and
// return 200 (skip silently) rather than treating this as an error.
var ErrUnsupportedEventKind = errors.New("gitlab webhook: unsupported event kind")

// GitLabWebhookEvent is the normalised IPC payload for all GitLab webhook events.
// The RawPayload field preserves the original JSON for downstream consumers that
// need fields not extracted at parse time.
type GitLabWebhookEvent struct {
	Source      string          `json:"source"`      // always "gitlab"
	EventKind   string          `json:"event_kind"`  // e.g. "Pipeline Hook"
	DeliveryID  string          `json:"delivery_id"` // X-Gitlab-Event-UUID or computed hash
	OccurredAt  time.Time       `json:"occurred_at"`
	ProjectID   int64           `json:"project_id"`
	ProjectURL  string          `json:"project_url"`
	RawPayload  json.RawMessage `json:"raw_payload"`
	ObjectKind  string          `json:"object_kind"`            // pipeline | merge_request | note | push
	ObjectState string          `json:"object_state,omitempty"` // opened | closed | merged | running | failed | etc.
	MRIID       int64           `json:"mr_iid,omitempty"`
	NoteID      int64           `json:"note_id,omitempty"`
	PipelineID  int64           `json:"pipeline_id,omitempty"`
}

// IPCEventName returns the IPC bus event name for the given object_kind.
func (e *GitLabWebhookEvent) IPCEventName() string {
	switch e.ObjectKind {
	case "pipeline":
		return "gitlab.pipeline"
	case "merge_request":
		return "gitlab.mr"
	case "note":
		return "gitlab.note"
	case "push":
		return "gitlab.push"
	default:
		return "gitlab.unknown"
	}
}

// ParseWebhookPayload parses the X-Gitlab-Event header and JSON body into a
// GitLabWebhookEvent. When deliveryID is empty a deterministic hash is computed
// from the payload to guarantee idempotency across GitLab instances older than
// 16.4 that do not send X-Gitlab-Event-UUID.
func ParseWebhookPayload(eventKind, deliveryID string, body []byte) (*GitLabWebhookEvent, error) {
	switch eventKind {
	case EventKindPipeline, EventKindMergeRequest, EventKindNote, EventKindPush:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEventKind, eventKind)
	}

	// Unmarshal only the discriminant fields; RawPayload keeps the rest.
	var raw struct {
		ObjectKind       string `json:"object_kind"`
		ObjectAttributes struct {
			State     string `json:"state"`
			IID       int64  `json:"iid"`
			NoteID    int64  `json:"note_id"`
			Action    string `json:"action"`
			CreatedAt string `json:"created_at"`
		} `json:"object_attributes"`
		MergeRequest struct {
			IID int64 `json:"iid"`
		} `json:"merge_request"`
		Project struct {
			ID          int64  `json:"id"`
			WebURL      string `json:"web_url"`
			HomepageURL string `json:"homepage"`
		} `json:"project"`
		// Pipeline fields
		Builds []struct{} `json:"builds"`
		// Push fields
		Commits []struct {
			Timestamp string `json:"timestamp"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gitlab webhook: unmarshal payload: %w", err)
	}

	if deliveryID == "" {
		deliveryID = computeDeliveryID(eventKind, raw.Project.ID, raw.ObjectAttributes.IID, body)
	}

	occurredAt := parseEventTime(eventKind, raw.ObjectAttributes.CreatedAt, raw.Commits)

	evt := &GitLabWebhookEvent{
		Source:     "gitlab",
		EventKind:  eventKind,
		DeliveryID: deliveryID,
		OccurredAt: occurredAt,
		ProjectID:  raw.Project.ID,
		ProjectURL: raw.Project.WebURL,
		RawPayload: json.RawMessage(body),
		ObjectKind: raw.ObjectKind,
	}

	switch eventKind {
	case EventKindMergeRequest:
		evt.ObjectState = raw.ObjectAttributes.State
		if raw.ObjectAttributes.IID != 0 {
			evt.MRIID = raw.ObjectAttributes.IID
		} else {
			evt.MRIID = raw.MergeRequest.IID
		}
	case EventKindNote:
		evt.ObjectState = raw.ObjectAttributes.Action
		evt.NoteID = raw.ObjectAttributes.NoteID
	case EventKindPipeline:
		evt.ObjectState = raw.ObjectAttributes.State
		evt.PipelineID = raw.ObjectAttributes.IID
	case EventKindPush:
		// push has no state discriminant
	}

	return evt, nil
}

// parseEventTime extracts the canonical event timestamp from the payload.
// GitLab does not include a request timestamp header, so we use
// object_attributes.created_at (MR, Note, Pipeline) or commits[0].timestamp (Push).
func parseEventTime(eventKind, createdAt string, commits []struct {
	Timestamp string `json:"timestamp"`
}) time.Time {
	if eventKind == EventKindPush && len(commits) > 0 {
		if t, err := time.Parse(time.RFC3339, commits[0].Timestamp); err == nil {
			return t
		}
	}
	if createdAt != "" {
		// GitLab uses both RFC3339 and "2006-01-02T15:04:05.000Z" variants.
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if t, err := time.Parse(layout, createdAt); err == nil {
				return t
			}
		}
	}
	return time.Now().UTC()
}

// computeDeliveryID returns a deterministic hex digest for instances that do
// not send X-Gitlab-Event-UUID (GitLab < 16.4).
func computeDeliveryID(eventKind string, projectID, objectIID int64, body []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s:%d:%d:", eventKind, projectID, objectIID)
	h.Write(body)
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}
