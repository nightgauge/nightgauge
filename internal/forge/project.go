package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// ProjectService is the forge-agnostic surface for project board / kanban
// operations. The shape closely follows GitHub Projects V2 — GitLab issue
// boards map to the same operations with an adapter-side translation.
type ProjectService interface {
	// Item membership.
	AddItem(ctx context.Context, contentNodeID string) (string, error)
	AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error)
	BulkAddIssues(ctx context.Context, owner, repo string, issues []forgetypes.Issue) forgetypes.BulkAddResult

	// Status routing.
	SyncStatus(ctx context.Context, owner, repo string, issueNumber int, status string) error
	MoveStatus(ctx context.Context, owner, repo string, issueNumber int, newStatus string) error
	SyncIteration(ctx context.Context, owner, repo string, issueNumber int, iteration string) error

	// Single-field setters by item ID.
	SetSingleSelectField(ctx context.Context, itemID, fieldName, optionName string) error
	SetNumberField(ctx context.Context, itemID, fieldName string, value float64) error
	SetTextField(ctx context.Context, itemID, fieldName, value string) error
	SetTextFieldOptional(ctx context.Context, itemID, fieldName, value string) error
	SetDateField(ctx context.Context, itemID, fieldName, dateValue string) error
	SetDateFieldOptional(ctx context.Context, itemID, fieldName, dateValue string) error
	SetIterationField(ctx context.Context, itemID, fieldName, iterationTitle string) error

	// Batch / number-keyed setters.
	SetFields(ctx context.Context, owner, repo string, issueNumber int, fields map[string]string) error
	SetHours(ctx context.Context, owner, repo string, issueNumber int, hours float64) error
	SetDateFieldByNumber(ctx context.Context, owner, repo string, issueNumber int, fieldName, dateValue string) error
	SetEstimateFromLabels(ctx context.Context, owner, repo string, issueNumber int, labels []string, mapping map[string]float64) error

	// Blocking / dependency edges.
	AddBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error
	RemoveBlockedByNumber(ctx context.Context, owner, repo string, blockedNumber, blockerNumber int) error

	// Epic aggregation.
	UpdateEpicEstimates(ctx context.Context, owner, repo string, epicNumber int) (float64, error)

	// Schema / drift management.
	EnsureFields(ctx context.Context, schema forgetypes.FieldSchema) (*forgetypes.EnsureFieldsResult, error)
	DriftCheck(ctx context.Context) ([]forgetypes.FieldDrift, error)
	DriftFix(ctx context.Context) ([]forgetypes.FieldDrift, error)
	SnapshotFields(ctx context.Context) (*forgetypes.FieldsSnapshot, error)
}

// BoardService is a read-only subset of the project board surface used by
// callers that only need to enumerate items (e.g. the state board snapshot).
type BoardService interface {
	ListItems(ctx context.Context, statusFilter string) ([]forgetypes.BoardItem, error)
	ListOpenItems(ctx context.Context) ([]forgetypes.BoardItem, int, error)
	CountsByStatus(ctx context.Context) (*forgetypes.StatusCounts, error)

	// GetItem fetches a single board item by issue number. Adapters return
	// ErrNotFound when the issue exists but is not on the bound board (or
	// the issue itself does not exist).
	GetItem(ctx context.Context, owner, repo string, issueNumber int) (*forgetypes.BoardItem, error)
}
