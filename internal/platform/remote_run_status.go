package platform

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RemoteRunStatus is the terminal status of a run the platform created when an
// agent acknowledged its trigger (#3586).
type RemoteRunStatus string

const (
	// RemoteRunSuccess is sent as `success: true`.
	RemoteRunSuccess RemoteRunStatus = "success"
	// RemoteRunFailure is sent as `success: false`.
	RemoteRunFailure RemoteRunStatus = "failure"
	// RemoteRunCancelled is sent with no `success`. Which run ends map to
	// which status is the caller's (headlesstrigger.Status).
	RemoteRunCancelled RemoteRunStatus = "cancelled"
)

// ErrInvalidRemoteRunID is returned for a run id that is not a UUID, which the
// platform's ingest schema would reject.
var ErrInvalidRemoteRunID = errors.New("remote run id is not a UUID")

var errUnknownRemoteRunStatus = errors.New("unknown remote run status")

// IsPermanentReportError reports whether a ReportRemoteRunTerminal error is
// one re-sending cannot fix: an invalid run id, an unknown status, or a 4xx
// refusal other than 429. Transport errors, 5xx and 429 are transient.
func IsPermanentReportError(err error) bool {
	return errors.Is(err, ErrInvalidRemoteRunID) || errors.Is(err, errUnknownRemoteRunStatus) ||
		isPermanentEventRejection(err)
}

// ReportRemoteRunTerminal closes the platform run remoteRunID, the run id an
// accepting trigger ack returned, with status (#3586). It posts one
// `pipeline_done` event for that id on the pipeline ingest path:
// `success: true` for success, `success: false` for failure, and no `success`
// for cancelled, which closes the row and keeps the status the platform's
// cancel already set (ADR-006). visibility is state.VisibilityPrivate for a
// private run, so the closing event says so like every other event of it.
//
// It is synchronous and unbuffered, and it is not behind the analytics send
// gate: like the ack it answers, it is the reply to a command the platform
// sent, not telemetry. The local run's own events stay keyed by its local id.
func (c *Client) ReportRemoteRunTerminal(ctx context.Context, remoteRunID string, status RemoteRunStatus, visibility string) error {
	if !telemetryRunIDPattern.MatchString(remoteRunID) {
		return ErrInvalidRemoteRunID
	}
	event := PipelineEvent{
		RunID:         remoteRunID,
		EventType:     "pipeline_done",
		Timestamp:     time.Now(),
		Visibility:    visibility,
		SchemaVersion: "1",
	}
	switch status {
	case RemoteRunSuccess:
		ok := true
		event.Success = &ok
	case RemoteRunFailure:
		ok := false
		event.Success = &ok
	case RemoteRunCancelled:
	default:
		return fmt.Errorf("%w %q", errUnknownRemoteRunStatus, status)
	}
	return (&AnalyticsService{client: c}).emitPipelineEventSync(ctx, event)
}
