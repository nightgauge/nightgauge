// Package state implements board-driven pipeline state management.
// GitHub Project Board fields are the durable state — no local files needed.
package state

import (
	"context"
	"fmt"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// PipelineStage represents a pipeline execution stage.
type PipelineStage string

const (
	StageIssuePickup     PipelineStage = "issue-pickup"
	StageFeaturePlanning PipelineStage = "feature-planning"
	StageFeatureDev      PipelineStage = "feature-dev"
	StageFeatureValidate PipelineStage = "feature-validate"
	StagePRCreate        PipelineStage = "pr-create"
	StagePRMerge         PipelineStage = "pr-merge"
	// StageSpikeMaterialize creates follow-up issues from a spike artifact's
	// recommendations YAML block. Appended after StagePRMerge for type:spike
	// issues only — see internal/orchestrator/scheduler.go.
	StageSpikeMaterialize PipelineStage = "spike-materialize"
)

// BoardStatus represents the project board status field values.
type BoardStatus string

const (
	StatusBacklog    BoardStatus = "Backlog"
	StatusReady      BoardStatus = "Ready"
	StatusInProgress BoardStatus = "In Progress"
	StatusInReview   BoardStatus = "In Review"
	StatusDone       BoardStatus = "Done"
)

// BoardStateService reads and writes pipeline state via GitHub Project Board fields.
// Write operations are delegated to an embedded ProjectService for cache unification.
type BoardStateService struct {
	client        *gh.Client
	owner         string
	ownerType     gh.OwnerType
	projectNumber int

	// projSvc handles all write operations (single cache, single write path)
	projSvc *gh.ProjectService
}

// NewBoardStateService creates a board state service.
// ownerType distinguishes organizations ("org") from user accounts ("user").
func NewBoardStateService(client *gh.Client, owner string, projectNumber int, ownerType ...gh.OwnerType) *BoardStateService {
	ot := gh.OwnerTypeOrg
	if len(ownerType) > 0 {
		ot = ownerType[0]
	}
	return &BoardStateService{
		client:        client,
		owner:         owner,
		ownerType:     ot,
		projectNumber: projectNumber,
		projSvc:       gh.NewProjectService(client, owner, projectNumber, ot),
	}
}

// SetStatus updates the board status for an item (e.g., Ready → In Progress → Done).
func (s *BoardStateService) SetStatus(ctx context.Context, itemID string, status BoardStatus) error {
	return s.projSvc.SetSingleSelectField(ctx, itemID, "Status", string(status))
}

// UpdateStatus updates the board status for an item using a plain string value.
func (s *BoardStateService) UpdateStatus(ctx context.Context, itemID, status string) error {
	return s.SetStatus(ctx, itemID, BoardStatus(status))
}

// SetPipelineStage updates the Pipeline Stage field for a board item.
func (s *BoardStateService) SetPipelineStage(ctx context.Context, itemID string, stage PipelineStage) error {
	return s.projSvc.SetTextFieldOptional(ctx, itemID, "Pipeline Stage", string(stage))
}

// GetPipelineStage reads the current pipeline stage from the board for crash recovery.
func (s *BoardStateService) GetPipelineStage(ctx context.Context, itemID string) (PipelineStage, error) {
	board := gh.NewBoardService(s.client, s.owner, s.projectNumber, s.ownerType)
	items, err := board.ListItems(ctx, "")
	if err != nil {
		return "", err
	}

	for _, item := range items {
		if item.ID == itemID && item.PipelineStage != "" {
			return PipelineStage(item.PipelineStage), nil
		}
	}

	return "", nil // No stage set
}

// StartPipeline sets the board status to "In Progress" and records the initial stage.
func (s *BoardStateService) StartPipeline(ctx context.Context, itemID string, stage PipelineStage) error {
	if err := s.SetStatus(ctx, itemID, StatusInProgress); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if err := s.SetPipelineStage(ctx, itemID, stage); err != nil {
		return fmt.Errorf("set stage: %w", err)
	}
	return nil
}

// CompletePipeline sets the board status to "In Review" or "Done" and clears the stage.
func (s *BoardStateService) CompletePipeline(ctx context.Context, itemID string, status BoardStatus) error {
	if err := s.SetStatus(ctx, itemID, status); err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	// Clear pipeline stage (non-fatal if field not present)
	_ = s.projSvc.SetTextFieldOptional(ctx, itemID, "Pipeline Stage", "")
	return nil
}

// FailPipeline reverts an issue's board status after a pipeline failure.
// targetStatus is the configured failure destination ("Ready" or "Backlog").
// If the issue is already "In Review" (a PR was opened before failure), the
// status is left unchanged to avoid disrupting the review workflow.
// Returns true if the status was actually changed.
func (s *BoardStateService) FailPipeline(ctx context.Context, itemID string, targetStatus BoardStatus) (bool, error) {
	// Read current status to guard against reverting an "In Review" issue.
	currentStatus, err := s.readItemStatus(ctx, itemID)
	if err != nil {
		// If we can't read current status, proceed with the revert — better to
		// move an issue back to Ready than leave it stuck In Progress.
		_ = err // non-fatal: proceed with revert
	} else if currentStatus == StatusInReview {
		return false, nil // PR is open; leave status as-is
	}

	if err := s.SetStatus(ctx, itemID, targetStatus); err != nil {
		return false, fmt.Errorf("revert status to %s: %w", targetStatus, err)
	}

	// Clear pipeline stage field (non-fatal if field not present)
	_ = s.projSvc.SetTextFieldOptional(ctx, itemID, "Pipeline Stage", "")

	return true, nil
}

// readItemStatus fetches the current board status for a specific item.
func (s *BoardStateService) readItemStatus(ctx context.Context, itemID string) (BoardStatus, error) {
	board := gh.NewBoardService(s.client, s.owner, s.projectNumber, s.ownerType)
	items, err := board.ListItems(ctx, "")
	if err != nil {
		return "", fmt.Errorf("fetch items for status check: %w", err)
	}
	for _, item := range items {
		if item.ID == itemID {
			return BoardStatus(item.Status), nil
		}
	}
	return "", fmt.Errorf("item %s not found on board", itemID)
}
