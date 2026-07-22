// Package runstate manages the durable pipeline lifecycle record at
// .nightgauge/pipeline/run-state.json. It is the single source of truth
// for whether a given issue's pipeline is running, paused, completed,
// discarded, or aborted — used by both the Go scheduler and the TypeScript
// SDK (via the same on-disk file format).
//
// Mirrors packages/nightgauge-sdk/src/context/schemas/run-state.ts
// field-for-field. The schema_version on disk gates compatibility — see
// docs/PIPELINE_STATE_SCHEMA.md for the full version history.
package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is the current major.minor on-disk schema version.
const SchemaVersion = "1.0"

// FileName is the canonical filename under .nightgauge/pipeline/.
const FileName = "run-state.json"

// Lifecycle is the enumerated set of states the run can be in.
type Lifecycle string

const (
	StateRunning   Lifecycle = "running"
	StatePaused    Lifecycle = "paused"
	StateCompleted Lifecycle = "completed"
	StateDiscarded Lifecycle = "discarded"
	StateAborted   Lifecycle = "aborted"
)

// Stage matches the pipeline stage strings used elsewhere in the codebase.
type Stage string

const (
	StageIssuePickup  Stage = "issue-pickup"
	StageFeaturePlan  Stage = "feature-planning"
	StageFeatureDev   Stage = "feature-dev"
	StageFeatureValid Stage = "feature-validate"
	StagePRCreate     Stage = "pr-create"
	StagePRMerge      Stage = "pr-merge"
)

// stageOrder is the canonical execution order; nextStage walks this list.
var stageOrder = []Stage{
	StageIssuePickup,
	StageFeaturePlan,
	StageFeatureDev,
	StageFeatureValid,
	StagePRCreate,
	StagePRMerge,
}

// Attempt records one start of the run. Resume creates a new attempt sharing
// the same RunID; restart creates a new RunID.
type Attempt struct {
	RunID         string  `json:"run_id"`
	AttemptNumber int     `json:"attempt_number"`
	StartedAt     string  `json:"started_at"`
	EndedAt       *string `json:"ended_at,omitempty"`
	PID           *int    `json:"pid,omitempty"`
	HostID        *string `json:"host_id,omitempty"`
	LastStage     *Stage  `json:"last_stage,omitempty"`
}

// RunState is the on-disk envelope. Field tags use snake_case to match the
// existing repo convention and the Zod schema in run-state.ts.
type RunState struct {
	SchemaVersion   string    `json:"schema_version"`
	IssueNumber     int       `json:"issue_number"`
	State           Lifecycle `json:"state"`
	RunID           string    `json:"run_id"`
	AttemptNumber   int       `json:"attempt_number"`
	CompletedStages []Stage   `json:"completed_stages"`
	ResumeFromStage *Stage    `json:"resume_from_stage,omitempty"`
	WorktreePath    *string   `json:"worktree_path,omitempty"`
	Branch          string    `json:"branch"`
	CreatedAt       string    `json:"created_at"`
	UpdatedAt       string    `json:"updated_at"`
	Reason          *string   `json:"reason,omitempty"`
	Recoverable     *bool     `json:"recoverable,omitempty"`
	RecoveryActions []string  `json:"recovery_actions,omitempty"`
	Attempts        []Attempt `json:"attempts"`
}

// Validate performs structural sanity checks. Used by Load before returning
// a RunState to callers; bypassed by tests that intentionally write malformed
// fixtures.
func (rs *RunState) Validate() error {
	if rs.SchemaVersion == "" {
		return fmt.Errorf("missing schema_version")
	}
	if !IsSchemaCompatible(rs.SchemaVersion, SchemaVersion) {
		return fmt.Errorf("schema_version %q not compatible with reader %q (see docs/PIPELINE_STATE_SCHEMA.md)",
			rs.SchemaVersion, SchemaVersion)
	}
	if rs.IssueNumber < 0 {
		return fmt.Errorf("issue_number must be non-negative, got %d", rs.IssueNumber)
	}
	switch rs.State {
	case StateRunning, StatePaused, StateCompleted, StateDiscarded, StateAborted:
	default:
		return fmt.Errorf("invalid state %q", rs.State)
	}
	if rs.RunID == "" {
		return fmt.Errorf("missing run_id")
	}
	if rs.AttemptNumber < 1 {
		return fmt.Errorf("attempt_number must be >= 1, got %d", rs.AttemptNumber)
	}
	if rs.Branch == "" {
		return fmt.Errorf("missing branch")
	}
	if len(rs.Attempts) == 0 {
		return fmt.Errorf("attempts must be non-empty")
	}
	return nil
}

// IsSchemaCompatible returns true when a file at version `file` can be safely
// read by a reader expecting version `expected`. Same major + file minor ≤
// expected minor. Future minors are rejected so we don't silently drop fields
// the writer added.
func IsSchemaCompatible(file, expected string) bool {
	fmaj, fmin, ok1 := splitMajorMinor(file)
	emaj, emin, ok2 := splitMajorMinor(expected)
	if !ok1 || !ok2 {
		return false
	}
	if fmaj != emaj {
		return false
	}
	return fmin <= emin
}

func splitMajorMinor(v string) (int, int, bool) {
	var maj, min int
	n, err := fmt.Sscanf(v, "%d.%d", &maj, &min)
	if err != nil || n != 2 {
		return 0, 0, false
	}
	return maj, min, true
}

// Path returns the canonical run-state.json path for a base directory
// (typically .nightgauge/pipeline).
func Path(baseDir string) string {
	return filepath.Join(baseDir, FileName)
}

// nextStage returns the stage following `s`, or nil at end-of-pipeline.
func nextStage(s Stage) *Stage {
	for i, x := range stageOrder {
		if x == s && i < len(stageOrder)-1 {
			n := stageOrder[i+1]
			return &n
		}
	}
	return nil
}

// hasStage reports whether `s` already appears in the slice.
func hasStage(slice []Stage, s Stage) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

// FileExists is a small convenience used by callers checking for prior
// pipeline state without parsing.
func FileExists(baseDir string) bool {
	_, err := os.Stat(Path(baseDir))
	return err == nil
}

// jsonMarshalIndent is a thin shim so tests can swap the encoder without
// pulling encoding/json into every test file.
func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
