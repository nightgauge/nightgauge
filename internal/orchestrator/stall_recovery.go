package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Adaptive stall-recovery (Issue #3005).
//
// On the first stall-kill in a run, the scheduler synthesizes a feedback
// signal, writes feedback-{N}.json, and rewinds once to feature-planning via
// the existing RetryEngine. The second stall-kill in the same run is terminal
// and carries a new failure_category.
//
// Cost-cap kills (#3002) are NEVER retried — see HasCostCapKillMarker below.
//
// Implementation lives in this file (not scheduler.go) to keep the heuristic
// deterministic and unit-testable in isolation. See ADR-004:
// docs/decisions/004-adaptive-stall-recovery.md.

// StallRetriedOutcome is the informational outcome name used when a run
// recovers cleanly from a stall via adaptive retry. Logged for grep-ability.
const StallRetriedOutcome = "STALL_RETRIED"

// StallKilledAfterRetryCategory is the failure_category value applied to the
// stage detail of a run that exhausts its single stall-retry slot and stalls
// again. Classified as `agent` in the SDK reliability classifier — the
// underlying issue remains agent-class even after the first retry consumed.
const StallKilledAfterRetryCategory = "stall-killed-after-retry"

// stallRecoveryRationalePrefix appears in every scheduler-synthesized
// feedback signal so audits can distinguish synthetic from agent-emitted
// signals.
const stallRecoveryRationalePrefix = "synthesized by scheduler on stall-kill"

// HasCostCapKillMarker reports whether the error text matches the substrings
// emitted by the per-stage cost-cap circuit breaker (Issue #3002). Cost-cap
// kills are never retried, even when the error also matches stall-kill
// heuristics. Case-insensitive.
func HasCostCapKillMarker(errorText string) bool {
	if errorText == "" {
		return false
	}
	t := strings.ToLower(errorText)
	return strings.Contains(t, "[cost-cap-exceeded]") ||
		strings.Contains(t, "cost-cap-exceeded") ||
		strings.Contains(t, "cost cap exceeded")
}

// CanRewindFromStage reports whether a stall-kill in `stage` can rewind to
// feature-planning. Only feature-dev and feature-validate satisfy this — the
// stages whose backtrack_target_stage is feature-planning. Stalls in
// issue-pickup, pr-create, and pr-merge fall through to the terminal path
// unchanged.
func CanRewindFromStage(stage state.PipelineStage) bool {
	return stage == state.StageFeatureDev || stage == state.StageFeatureValidate
}

// ClassifyStallSignal returns a deterministic synthetic feedback signal for a
// stall-kill on `killedStage`. The classifier reads `planning-{N}.json` to
// inspect the plan that was in flight when the stall happened.
//
// Heuristic priority order:
//  1. files_to_modify length >= 4 → COMPLEXITY_UNDERESTIMATED
//  2. planning context references files that do not exist on disk →
//     SCOPE_DISCOVERED (with the missing file paths as evidence)
//  3. Fallback → PLAN_REVISION_NEEDED
//
// All three branches emit `severity: blocking` and target feature-planning.
// Pure-ish: the only side effect is reading planning-{N}.json. Returns a fully
// populated FeedbackSignal even when the plan file is missing — fallback path.
func ClassifyStallSignal(killedStage state.PipelineStage, errorText string, workspaceRoot string, issueNumber int) FeedbackSignal {
	rationale := fmt.Sprintf("%s in %s — error: %s", stallRecoveryRationalePrefix, killedStage, truncateError(errorText, 200))
	signal := FeedbackSignal{
		EmittedByStage:       string(killedStage),
		BacktrackTargetStage: string(state.StageFeaturePlanning),
		Severity:             "blocking",
		Evidence:             []string{fmt.Sprintf("stall-kill in %s: %s", killedStage, truncateError(errorText, 500))},
		Rationale:            rationale,
		// Default to PLAN_REVISION_NEEDED — the conservative fallback.
		SignalType: "PLAN_REVISION_NEEDED",
	}

	// Only feature-dev and feature-validate get the more specific
	// classifications — those are the only stages whose plan we can inspect.
	if !CanRewindFromStage(killedStage) {
		return signal
	}

	planningPath := filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("planning-%d.json", issueNumber))
	data, err := os.ReadFile(planningPath)
	if err != nil {
		// Plan file missing — fall through to PLAN_REVISION_NEEDED.
		return signal
	}

	var plan struct {
		FilesToModify []string `json:"files_to_modify"`
		FilesToCreate []string `json:"files_to_create"`
	}
	if jerr := json.Unmarshal(data, &plan); jerr != nil {
		return signal
	}

	// Rule 1: high modify count → underestimated complexity.
	if len(plan.FilesToModify) >= 4 {
		signal.SignalType = "COMPLEXITY_UNDERESTIMATED"
		signal.Rationale = fmt.Sprintf("%s; plan modifies %d files (>=4 threshold)",
			stallRecoveryRationalePrefix, len(plan.FilesToModify))
		signal.Evidence = append(signal.Evidence,
			fmt.Sprintf("planning-%d.json files_to_modify count: %d", issueNumber, len(plan.FilesToModify)))
		return signal
	}

	// Rule 2: planning references files that do not exist → scope discovered.
	missing := missingFiles(plan.FilesToModify, workspaceRoot)
	if len(missing) > 0 {
		signal.SignalType = "SCOPE_DISCOVERED"
		signal.Rationale = fmt.Sprintf("%s; %d planned files missing from disk",
			stallRecoveryRationalePrefix, len(missing))
		signal.Evidence = append(signal.Evidence, missing...)
		return signal
	}

	return signal
}

// missingFiles returns the subset of `paths` that do not exist under
// workspaceRoot. Paths are interpreted as relative to workspaceRoot when
// non-absolute.
func missingFiles(paths []string, workspaceRoot string) []string {
	var out []string
	for _, p := range paths {
		full := p
		if !filepath.IsAbs(full) {
			full = filepath.Join(workspaceRoot, p)
		}
		if _, err := os.Stat(full); os.IsNotExist(err) {
			out = append(out, fmt.Sprintf("missing: %s", p))
		}
	}
	return out
}

// truncateError caps the error text at maxLen characters with an ellipsis. The
// rationale and evidence fields surface in dashboard tooltips, so unbounded
// stack traces would balloon the JSONL.
func truncateError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// WriteSyntheticFeedbackContext writes feedback-{N}.json containing the
// synthesized signal. Reuses the existing FeedbackContext struct so the file
// round-trips through `RetryEngine.EvaluateBacktrack` and the SDK Zod schema
// in `packages/nightgauge-sdk/src/context/schemas/feedback.ts`.
//
// schema_version mirrors the value used by the SDK schema regex (^\d+\.\d+$).
func WriteSyntheticFeedbackContext(workspaceRoot string, issueNumber int, signal FeedbackSignal) error {
	if workspaceRoot == "" {
		return fmt.Errorf("workspaceRoot is required")
	}
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pipeline dir: %w", err)
	}

	ctx := FeedbackContext{
		SchemaVersion: "1.0",
		IssueNumber:   issueNumber,
		Signals:       []FeedbackSignal{signal},
	}
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal feedback: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("feedback-%d.json", issueNumber))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp feedback: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename feedback: %w", err)
	}
	return nil
}

// GetAdaptiveStallRecoveryEnabled reads `pipeline.adaptive_stall_recovery`
// from `.nightgauge/config.yaml` with env-var override.
//
// #3020 — flipped default from false → true. Originally opt-in to limit
// blast radius during rollout; the original incident burned $18.96 because
// a stall-killed feature-validate went straight to terminal failure with
// nobody having flipped the flag. The retry-once-per-run cap (#3015) makes
// this safe to enable by default.
//
// Env override: NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY
//
// Accepted values:
//   - "true", "1", "yes" → true
//   - "false", "0", "no" → false
//   - anything else / unset → DEFAULT (true)
func GetAdaptiveStallRecoveryEnabled(workspaceRoot string) bool {
	const defaultEnabled = true
	if v := os.Getenv("NIGHTGAUGE_PIPELINE_ADAPTIVE_STALL_RECOVERY"); v != "" {
		return parseBoolishOrDefault(v, defaultEnabled)
	}
	if workspaceRoot == "" {
		return defaultEnabled
	}
	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return defaultEnabled
	}
	inPipeline := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "pipeline:" {
			inPipeline = true
			continue
		}
		if inPipeline && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = false
		}
		if inPipeline && strings.HasPrefix(trimmed, "adaptive_stall_recovery:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"'`)
				return parseBoolishOrDefault(val, defaultEnabled)
			}
		}
	}
	return defaultEnabled
}

func parseBoolish(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// parseBoolishOrDefault is like parseBoolish but returns `defaultVal` for
// unrecognised input rather than always falling through to false. Lets
// adaptive_stall_recovery flip its default without rewriting truthy parsing.
func parseBoolishOrDefault(s string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return defaultVal
}
