package orchestrator

// Backing primitives for the two Action Center verbs the fleet lacked before
// ADR 015 §B: `budget.raiseCeiling` and `run.retryWithEscalation`. Each is a
// thin, deterministic, audited operation fronting an existing enforcement path
// — never new business logic. Both persist a small runtime override under
// `.nightgauge/pipeline/` (atomic temp+rename, the same carrier stall recovery
// and the queue writer use) that an existing dispatch/enforcement site honors.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// --- budget.raiseCeiling -----------------------------------------------------
//
// The USD pipeline ceiling is a config value resolved once per run by
// getPipelineBudgetCeilingUSD and enforced terminally (budget_ceiling_hit). A
// resolution of the budget-ceiling DecisionRequest writes a RUNTIME override
// that getPipelineBudgetCeilingUSD reads and takes as the effective ceiling —
// so the next dispatch runs under the raised ceiling without editing config.

const budgetOverrideRelPath = ".nightgauge/pipeline/budget-override.json"

// BudgetCeilingOverride is the persisted runtime ceiling raise.
type BudgetCeilingOverride struct {
	SchemaVersion int     `json:"schema_version"`
	CeilingUSD    float64 `json:"ceiling_usd"`
	RaisedBy      string  `json:"raised_by,omitempty"`
	RaisedAt      string  `json:"raised_at"`
	Reason        string  `json:"reason,omitempty"`
}

func budgetOverridePath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, budgetOverrideRelPath)
}

// WriteBudgetCeilingOverride persists a runtime USD ceiling override. Honored by
// getPipelineBudgetCeilingUSD (which takes the max of config and override) so a
// raised ceiling lets the run continue past the previous cap. Atomic
// temp+rename — a concurrent reader never sees a half-written file.
func WriteBudgetCeilingOverride(workspaceRoot string, ceilingUSD float64, actor, reason string) error {
	if workspaceRoot == "" {
		return fmt.Errorf("budget.raiseCeiling: workspaceRoot is required")
	}
	if ceilingUSD <= 0 {
		return fmt.Errorf("budget.raiseCeiling: ceiling must be positive, got %.2f", ceilingUSD)
	}
	ov := BudgetCeilingOverride{
		SchemaVersion: 1,
		CeilingUSD:    ceilingUSD,
		RaisedBy:      actor,
		RaisedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		Reason:        reason,
	}
	data, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return fmt.Errorf("budget.raiseCeiling: marshal: %w", err)
	}
	path := budgetOverridePath(workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("budget.raiseCeiling: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("budget.raiseCeiling: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("budget.raiseCeiling: rename: %w", err)
	}
	return nil
}

// readBudgetCeilingOverrideUSD returns the persisted runtime ceiling override in
// USD, or 0 when none is set / unreadable.
func readBudgetCeilingOverrideUSD(workspaceRoot string) float64 {
	if workspaceRoot == "" {
		return 0
	}
	data, err := os.ReadFile(budgetOverridePath(workspaceRoot))
	if err != nil {
		return 0
	}
	var ov BudgetCeilingOverride
	if json.Unmarshal(data, &ov) != nil {
		return 0
	}
	if ov.CeilingUSD <= 0 {
		return 0
	}
	return ov.CeilingUSD
}

// --- run.retryWithEscalation -------------------------------------------------
//
// Model escalation is automatic inside failure handling and never
// operator-invokable. A resolution of the watchdog DecisionRequest writes a
// consume-once per-issue override forcing a model tier for the next run; the
// scheduler applies and clears it at run start (runPipeline). The verb pairs
// this with clearing the failure cooldown (autonomous.clearIssueFailures) and a
// re-dispatch so the escalated retry actually runs.

// EscalationOverride is the persisted, consume-once forced model tier.
type EscalationOverride struct {
	SchemaVersion int    `json:"schema_version"`
	Tier          string `json:"tier"`
	ForcedBy      string `json:"forced_by,omitempty"`
	ForcedAt      string `json:"forced_at"`
}

func escalationOverridePath(workspaceRoot string, issue int) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("escalation-override-%d.json", issue))
}

// WriteEscalationOverride persists a per-issue forced model tier (e.g. "opus"),
// applied and cleared by the scheduler at the next run start.
func WriteEscalationOverride(workspaceRoot string, issue int, tier, actor string) error {
	if workspaceRoot == "" {
		return fmt.Errorf("run.retryWithEscalation: workspaceRoot is required")
	}
	if tier == "" {
		return fmt.Errorf("run.retryWithEscalation: tier is required")
	}
	ov := EscalationOverride{
		SchemaVersion: 1,
		Tier:          tier,
		ForcedBy:      actor,
		ForcedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return fmt.Errorf("run.retryWithEscalation: marshal: %w", err)
	}
	path := escalationOverridePath(workspaceRoot, issue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("run.retryWithEscalation: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("run.retryWithEscalation: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("run.retryWithEscalation: rename: %w", err)
	}
	return nil
}

// ConsumeEscalationOverride returns the forced tier for an issue and removes the
// override (consume-once) so the escalation applies to the next run only.
// Returns ("", false) when no override is set.
func ConsumeEscalationOverride(workspaceRoot string, issue int) (string, bool) {
	if workspaceRoot == "" {
		return "", false
	}
	path := escalationOverridePath(workspaceRoot, issue)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var ov EscalationOverride
	if json.Unmarshal(data, &ov) != nil || ov.Tier == "" {
		os.Remove(path)
		return "", false
	}
	os.Remove(path) // consume-once
	return ov.Tier, true
}
