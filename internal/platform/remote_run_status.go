package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RemoteRunStatus is the terminal status of the platform run a trigger's ack
// returned (#3586); the daemon's start event (ReportRemoteRunStart) creates
// that run (#3699).
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

// errInvalidRemoteRunTarget is returned for a start event whose repo is not
// owner/name or whose issue number is not positive.
var errInvalidRemoteRunTarget = errors.New("remote run start needs an owner/name repo and a positive issue number")

// IsPermanentReportError reports whether a ReportRemoteRunStart or
// ReportRemoteRunTerminal error is one re-sending cannot fix: an invalid run
// id, an invalid repo or issue, an unknown status, or a 4xx
// refusal other than 429. Transport errors, 5xx and 429 are transient.
func IsPermanentReportError(err error) bool {
	return errors.Is(err, ErrInvalidRemoteRunID) || errors.Is(err, errUnknownRemoteRunStatus) ||
		errors.Is(err, errInvalidRemoteRunTarget) ||
		isPermanentEventRejection(err)
}

// ReportRemoteRunStart creates the platform run remoteRunID, which
// ReportRemoteRunTerminal later closes (#3699). The platform materialises a
// run row only from a start event naming repo and issue, so without it the
// terminal report is dropped as "Pipeline run not found". The ingest contract
// has no pipeline_start type: the run-creating event is the first
// `stage_started`, sent here for stage issue-pickup with repo ("owner/name"),
// issueNumber and origin local_cli. visibility is state.VisibilityPrivate for
// a private run.
//
// Like the terminal report it is synchronous, unbuffered and not behind the
// analytics send gate.
func (c *Client) ReportRemoteRunStart(ctx context.Context, remoteRunID, repo string, issueNumber int, visibility string) error {
	if !telemetryRunIDPattern.MatchString(remoteRunID) {
		return ErrInvalidRemoteRunID
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") || issueNumber <= 0 {
		return fmt.Errorf("%w (repo %q, issue %d)", errInvalidRemoteRunTarget, repo, issueNumber)
	}
	event := PipelineEvent{
		RunID:         remoteRunID,
		EventType:     "stage_started",
		Stage:         "issue-pickup",
		Repo:          repo,
		IssueNumber:   issueNumber,
		Origin:        "local_cli",
		Timestamp:     time.Now(),
		Visibility:    visibility,
		SchemaVersion: "1",
	}
	return (&AnalyticsService{client: c}).emitPipelineEventSync(ctx, event)
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
