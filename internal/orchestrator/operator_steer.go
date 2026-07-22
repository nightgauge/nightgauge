package orchestrator

// Operator steer (ADR 015 §G). Free-text steer typed on an Action Center
// resolution becomes pinned next-stage CONTEXT, never a command: it rides the
// existing feedback-context path (ADR 004) as a new OPERATOR_STEER signal at
// `warning` severity, so RetryEngine.EvaluateBacktrack (which acts only on
// `blocking` signals with a non-empty target) ignores it — the steer never
// triggers a rewind, it is pure background the next stage must honor.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// OperatorSteerSignalType is the closed signal_type enum entry for operator
// steer. Mirrors the SDK Zod enum in
// packages/nightgauge-sdk/src/context/schemas/feedback.ts.
const OperatorSteerSignalType = "OPERATOR_STEER"

// operatorSteerOriginMarker is the operator-origin evidence marker — the
// analogue of stallRecoveryRationalePrefix. It distinguishes operator steering
// from agent- and scheduler-synthesized signals in audits and the next stage's
// feedback-intake. (The signal's emitted_by_stage must be one of the six
// pipeline stages to satisfy the SDK Zod schema, so provenance is carried here
// rather than in the stage field.)
const operatorSteerOriginMarker = "operator-origin: action-center"

// defaultOperatorSteerStage is the emitted_by_stage used when the request has no
// valid stage context — feature-dev is a re-entrant stage that always consumes
// pinned context.
const defaultOperatorSteerStage = "feature-dev"

var pipelineStages = map[string]bool{
	"issue-pickup":     true,
	"feature-planning": true,
	"feature-dev":      true,
	"feature-validate": true,
	"pr-create":        true,
	"pr-merge":         true,
}

// WriteOperatorSteer appends an OPERATOR_STEER signal to the target issue's
// feedback-{N}.json (read-merge-write, the same transient carrier stall
// recovery and the pr-merge retry use). severity=warning and an empty
// backtrack target guarantee the steer is context-only and never rewinds.
// stageHint attributes the signal to the run's current/target stage when it is
// one of the six pipeline stages; otherwise it defaults to feature-dev.
func WriteOperatorSteer(workspaceRoot string, issueNumber int, steerText, stageHint string) error {
	if workspaceRoot == "" {
		return fmt.Errorf("operator steer: workspaceRoot is required")
	}
	if steerText == "" {
		return nil // nothing to pin
	}
	stage := stageHint
	if !pipelineStages[stage] {
		stage = defaultOperatorSteerStage
	}
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("operator steer: mkdir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("feedback-%d.json", issueNumber))

	// Read-merge-write: preserve any signals already pinned for this run.
	var ctx FeedbackContext
	if data, rerr := os.ReadFile(path); rerr == nil {
		_ = json.Unmarshal(data, &ctx) // tolerate a malformed/absent prior file
	}
	if ctx.SchemaVersion == "" {
		ctx.SchemaVersion = "1.0"
	}
	ctx.IssueNumber = issueNumber
	ctx.Signals = append(ctx.Signals, FeedbackSignal{
		SignalType:           OperatorSteerSignalType,
		EmittedByStage:       stage,
		BacktrackTargetStage: "", // no rewind — context only
		Severity:             "warning",
		Rationale:            steerText,
		Evidence:             []string{operatorSteerOriginMarker},
	})

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("operator steer: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("operator steer: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("operator steer: rename: %w", err)
	}
	return nil
}
