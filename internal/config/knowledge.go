package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// KnowledgeConfig holds knowledge base settings from the `knowledge:` section
// of config.yaml. These settings control scaffolding, validation gates, and
// enrichment behavior during planning.
type KnowledgeConfig struct {
	// Enabled controls whether the knowledge base is active. Defaults to false (opt-in).
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
	// AutoScaffold controls whether the knowledge directory is automatically
	// scaffolded at issue pickup when Enabled is true. Defaults to false.
	AutoScaffold *bool `yaml:"auto_scaffold" json:"auto_scaffold,omitempty"`
	// WikiLinks controls whether [[wiki-link]] syntax is resolved in knowledge documents.
	WikiLinks *bool `yaml:"wiki_links" json:"wiki_links,omitempty"`
	// RequireDecisions controls whether the decisions.md validation gate is enforced
	// during planning when the plan contains tradeoff signals. Defaults to false for
	// backward compatibility; new projects should set this to true.
	RequireDecisions *bool `yaml:"require_decisions" json:"require_decisions,omitempty"`
	// WorkspaceScoped controls whether the workspace-level KB tree
	// (product/, cross-repo/, architecture/) is auto-scaffolded at issue-pickup.
	// Defaults to true, but is gated by Enabled — workspace_scoped=true with
	// enabled=false is a no-op.
	WorkspaceScoped *bool `yaml:"workspace_scoped" json:"workspace_scoped,omitempty"`
	// Telemetry controls the knowledge telemetry emitter — one JSONL event per
	// KB operation written to .nightgauge/pipeline/history/knowledge-events.jsonl.
	// Resolved via IsTelemetryEnabled(): defaults on when Enabled is true and
	// Telemetry.Enabled is unset; always off when Enabled is false.
	Telemetry *KnowledgeTelemetryConfig `yaml:"telemetry" json:"telemetry,omitempty"`
	// Recall holds BM25 tuning parameters for `knowledge recall`.
	Recall *RecallConfig `yaml:"recall" json:"recall,omitempty"`
}

// KnowledgeTelemetryConfig nests under KnowledgeConfig.Telemetry and toggles
// the knowledge-events.jsonl emitter. The pointer-based Enabled field follows
// the same nil/default convention as the parent KnowledgeConfig so absent
// YAML means "use the resolver default" rather than "disabled".
type KnowledgeTelemetryConfig struct {
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
}

// RecallConfig holds BM25 tuning parameters for the `knowledge recall` command.
// Nested under KnowledgeConfig as knowledge.recall.* in config.yaml.
type RecallConfig struct {
	// BM25K1 controls term frequency saturation (default 1.5).
	BM25K1 *float64 `yaml:"bm25_k1" json:"bm25_k1,omitempty"`
	// BM25B controls document length normalization (default 0.75).
	BM25B *float64 `yaml:"bm25_b" json:"bm25_b,omitempty"`
	// PlanningThreshold is the minimum BM25 score for a recalled decision to be
	// injected into the plan. 0.0 means inject all results. Default: 0.0.
	PlanningThreshold *float64 `yaml:"planning_threshold" json:"planning_threshold,omitempty"`
	// PlanningLimit is the maximum number of recalled decisions to inject.
	// Default: 5.
	PlanningLimit *int `yaml:"planning_limit" json:"planning_limit,omitempty"`
}

// IsTelemetryEnabled returns the effective knowledge telemetry setting.
//
// The resolver enforces the safety rule from ADR-005 (issue #3592): telemetry
// is OFF whenever the parent knowledge.enabled is false, regardless of the
// nested telemetry.enabled value. This prevents projects that explicitly opt
// out of the KB from accidentally writing knowledge-events.jsonl.
//
// When knowledge.enabled is true:
//   - telemetry.enabled unset → returns true  (default on once KB is on)
//   - telemetry.enabled explicit → returns the explicit value
func (k *KnowledgeConfig) IsTelemetryEnabled() bool {
	if k == nil || !k.IsEnabled() {
		return false
	}
	if k.Telemetry == nil || k.Telemetry.Enabled == nil {
		return true
	}
	return *k.Telemetry.Enabled
}

// IsWorkspaceScoped returns the effective workspace_scoped setting. Defaults
// to true when unset so users who opt into the knowledge base also opt into
// the workspace-level tree by default.
func (k *KnowledgeConfig) IsWorkspaceScoped() bool {
	if k == nil || k.WorkspaceScoped == nil {
		return true
	}
	return *k.WorkspaceScoped
}

// IsEnabled returns true when knowledge base is enabled (opt-in, defaults to false).
func (k *KnowledgeConfig) IsEnabled() bool {
	if k == nil || k.Enabled == nil {
		return false
	}
	return *k.Enabled
}

// ResolveRequireDecisions returns the effective require_decisions setting.
// Defaults to false for backward compatibility with existing projects.
// New project configs should set knowledge.require_decisions: true explicitly.
func (k *KnowledgeConfig) ResolveRequireDecisions() bool {
	if k == nil || k.RequireDecisions == nil {
		return false
	}
	return *k.RequireDecisions
}

// RecallBM25K1 returns the effective BM25 k1 parameter (default 1.5).
func (k *KnowledgeConfig) RecallBM25K1() float64 {
	if k != nil && k.Recall != nil && k.Recall.BM25K1 != nil {
		return *k.Recall.BM25K1
	}
	return 1.5
}

// RecallBM25B returns the effective BM25 b parameter (default 0.75).
func (k *KnowledgeConfig) RecallBM25B() float64 {
	if k != nil && k.Recall != nil && k.Recall.BM25B != nil {
		return *k.Recall.BM25B
	}
	return 0.75
}

// RecallPlanningThreshold returns the minimum BM25 score for injecting a
// recalled decision into the feature-planning plan. Default: 0.0 (inject all).
func (k *KnowledgeConfig) RecallPlanningThreshold() float64 {
	if k != nil && k.Recall != nil && k.Recall.PlanningThreshold != nil {
		return *k.Recall.PlanningThreshold
	}
	return 0.0
}

// RecallPlanningLimit returns the maximum number of recalled decisions to
// inject into the feature-planning plan. Default: 5.
func (k *KnowledgeConfig) RecallPlanningLimit() int {
	if k != nil && k.Recall != nil && k.Recall.PlanningLimit != nil {
		return *k.Recall.PlanningLimit
	}
	return 5
}

// defaultTradeoffKeywords is the built-in fallback when the YAML file is missing.
var defaultTradeoffKeywords = []string{
	"tradeoff",
	"trade-off",
	"chose",
	"rejected",
	"considered",
	"alternative",
	"instead of",
	"in favor of",
	"decided against",
	"opted for",
}

// DefaultTradeoffKeywords returns a copy of the built-in default keyword list.
// Callers should use LoadTradeoffKeywords when a workspace root is available so
// the externalized YAML file is respected.
func DefaultTradeoffKeywords() []string {
	out := make([]string, len(defaultTradeoffKeywords))
	copy(out, defaultTradeoffKeywords)
	return out
}

// keywordsFile is the YAML format read by LoadTradeoffKeywords.
type keywordsFile struct {
	Keywords []string `yaml:"keywords"`
}

// LoadTradeoffKeywords loads the tradeoff keyword list from
// configs/knowledge-tradeoff-keywords.yaml inside workspaceRoot.
// Falls back to DefaultTradeoffKeywords when the file does not exist.
// Returns an error only for malformed YAML (not missing file).
func LoadTradeoffKeywords(workspaceRoot string) ([]string, error) {
	yamlPath := filepath.Join(workspaceRoot, "configs", "knowledge-tradeoff-keywords.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultTradeoffKeywords(), nil
		}
		return nil, fmt.Errorf("read tradeoff keywords file: %w", err)
	}

	var kf keywordsFile
	if err := yaml.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("parse tradeoff keywords file: %w", err)
	}

	if len(kf.Keywords) == 0 {
		return DefaultTradeoffKeywords(), nil
	}
	return kf.Keywords, nil
}
