// Package config handles CLI configuration loading.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"gopkg.in/yaml.v3"
)

// RemoteCommandsConfig holds remote command polling settings.
type RemoteCommandsConfig struct {
	// PollInterval is the base interval between command polls. Default: 5s.
	PollInterval time.Duration `yaml:"poll_interval" json:"pollInterval,omitempty"`
	// MaxBackoff is the maximum backoff interval on errors. Default: 60s.
	MaxBackoff time.Duration `yaml:"max_backoff" json:"maxBackoff,omitempty"`
	// Enabled controls whether command polling is active. Defaults to true
	// when platform credentials are configured.
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
}

// IsEnabled returns true unless explicitly disabled.
func (r *RemoteCommandsConfig) IsEnabled() bool {
	if r == nil || r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

// TelemetryConfig holds platform telemetry settings.
type TelemetryConfig struct {
	// Enabled controls whether the Go scheduler pushes run records to the platform.
	// Defaults to false when unset — telemetry is opt-in on every surface
	// (see docs/TELEMETRY_PRIVACY.md).
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
}

// IsEnabled reports whether platform telemetry is enabled. Telemetry is opt-in:
// it returns false unless explicitly turned on.
func (t *TelemetryConfig) IsEnabled() bool {
	if t == nil || t.Enabled == nil {
		return false // opt-in: off unless explicitly enabled
	}
	return *t.Enabled
}

// FeedbackLoopConfig holds health monitoring and feedback loop settings.
// All thresholds are read from the feedback_loop: section of config.yaml.
// Zero values mean "not configured"; callers must apply their own defaults.
type FeedbackLoopConfig struct {
	WarningThreshold   float64 `yaml:"health_warning_threshold"   json:"healthWarningThreshold,omitempty"`
	CriticalThreshold  float64 `yaml:"health_critical_threshold"  json:"healthCriticalThreshold,omitempty"`
	EmergencyThreshold float64 `yaml:"health_emergency_threshold" json:"healthEmergencyThreshold,omitempty"`
	ActionsEnabled     *bool   `yaml:"health_actions_enabled"     json:"healthActionsEnabled,omitempty"`
	PoliciesEnabled    *bool   `yaml:"health_policies_enabled"    json:"healthPoliciesEnabled,omitempty"`
	AutoRetroactive    *bool   `yaml:"auto_retroactive"           json:"autoRetroactive,omitempty"`
}

// SanitizationMode controls the firewall enforcement level.
type SanitizationMode string

const (
	SanitizationModeWarn     SanitizationMode = "warn"
	SanitizationModeBlock    SanitizationMode = "block"
	SanitizationModeDisabled SanitizationMode = "disabled"
)

// SanitizationConfig holds firewall settings from config.yaml.
type SanitizationConfig struct {
	Mode SanitizationMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Legacy field — if Mode is empty, check this for backward compat.
	WarnOnly *bool `json:"warn_only,omitempty" yaml:"warn_only,omitempty"`
}

// ResolvedMode returns the effective sanitization mode.
// Priority: Mode field > WarnOnly legacy field > default (warn).
func (s *SanitizationConfig) ResolvedMode() SanitizationMode {
	if s == nil {
		return SanitizationModeWarn
	}
	if s.Mode != "" {
		return s.Mode
	}
	if s.WarnOnly != nil {
		if *s.WarnOnly {
			return SanitizationModeWarn
		}
		return SanitizationModeBlock
	}
	return SanitizationModeWarn
}

// GitHubAuthConfig holds org-to-user fallback mappings for multi-identity workspaces.
// Used when per-repo github_user is not set — resolves the gh CLI user for each org.
//
// Token resolution priority (highest to lowest):
//  1. GITHUB_TOKEN env var (CI/CD override)
//  2. --token CLI flag (one-shot override)
//  3. Token field (per-project PAT, this struct)
//  4. Tokens[owner] (per-org PAT mapping, global config)
//  5. gh auth token --user <user> (gh CLI fallback, deprecated)
//  6. gh auth token (default gh CLI user, deprecated)
//
// Token values support env:VAR_NAME syntax to avoid plaintext PATs in YAML.
// Example: token: env:GITHUB_TOKEN_NIGHTGAUGE
type GitHubAuthConfig struct {
	// Users maps owner name (org or user) to GitHub username.
	// Example: {"nightgauge": "octocat", "Acme-Community": "acmebot"}
	Users map[string]string `yaml:"users" json:"users,omitempty"`

	// Token is a per-project GitHub PAT. Use env:VAR_NAME syntax to avoid
	// storing plaintext tokens in version control.
	// Example: token: env:GITHUB_TOKEN_NIGHTGAUGE
	Token string `yaml:"token" json:"token,omitempty"`

	// Tokens maps org/owner names to GitHub PATs for multi-org workspaces.
	// Typically set in global config (~/.nightgauge/config.yaml).
	// Use env:VAR_NAME syntax for each value.
	// Example: {"nightgauge": "env:GITHUB_TOKEN_NIGHTGAUGE"}
	Tokens map[string]string `yaml:"tokens" json:"tokens,omitempty"`

	// SuppressGHWarning suppresses the deprecation warning emitted when the
	// pipeline falls back to gh CLI for token resolution. Set to true when
	// intentionally using gh CLI as the token source.
	SuppressGHWarning bool `yaml:"suppress_gh_warning" json:"suppressGHWarning,omitempty"`
}

// PipelineExecutorConfig controls which execution substrate runs pipeline stages.
// Configured via pipeline.executor in config.yaml.
type PipelineExecutorConfig struct {
	// Type selects the execution substrate: "local" (default) or "cloud".
	// "local" runs pipeline stages on the developer's machine via the
	// TypeScript extension or Go scheduler queue.
	// "cloud" dispatches to the platform /v1/pipeline/dispatch endpoint where
	// a Durable Object runs stages without requiring a local machine.
	Type string `yaml:"type" json:"type,omitempty"`
}

// ExecutorType returns the resolved executor type, defaulting to "local".
func (p *PipelineExecutorConfig) ExecutorType() string {
	if p == nil || p.Type == "" {
		return "local"
	}
	return p.Type
}

// AgentTeamsConfig holds wave orchestration scaling settings.
type AgentTeamsConfig struct {
	// MaxConcurrent is the hard ceiling on parallel subagents per wave.
	// Default: 6, Range: 1–12.
	MaxConcurrent int `yaml:"max_concurrent" json:"maxConcurrent,omitempty"`
	// MinBudgetPerAgent is the minimum token budget each agent needs to be viable.
	// If remaining budget / concurrency < this value, concurrency is reduced.
	// Default: 100000 (100K tokens).
	MinBudgetPerAgent int64 `yaml:"min_budget_per_agent" json:"minBudgetPerAgent,omitempty"`
}

// DefaultAgentTeamsConfig returns default agent teams scaling values.
func DefaultAgentTeamsConfig() *AgentTeamsConfig {
	return &AgentTeamsConfig{
		MaxConcurrent:     6,
		MinBudgetPerAgent: 100_000,
	}
}

// SafetyRailsConfig holds safety rail thresholds for autonomous execution.
type SafetyRailsConfig struct {
	// BudgetCeiling is the global token limit across all pipeline runs. 0 = unlimited.
	BudgetCeiling int64 `yaml:"budget_ceiling" json:"budgetCeiling,omitempty"`
	// CircuitBreakerMax is the consecutive failure threshold. 0 = disabled.
	CircuitBreakerMax int `yaml:"circuit_breaker_max" json:"circuitBreakerMax,omitempty"`
	// RateLimitPerHour is the max pipeline starts per hour. 0 = disabled.
	RateLimitPerHour int `yaml:"rate_limit_per_hour" json:"rateLimitPerHour,omitempty"`
	// EpicCheckpoint pauses between epics for human review. Default: true.
	EpicCheckpoint bool `yaml:"epic_checkpoint" json:"epicCheckpoint,omitempty"`
	// HealthGateMin is the minimum health score (0–100) to continue. 0 = disabled.
	HealthGateMin int `yaml:"health_gate_min" json:"healthGateMin,omitempty"`
}

// DisciplineGateConfig is the autonomous.discipline_gate: block (#4100). The
// per-repo verification-readiness score gates full autonomy on an under-prepared
// repo (no real test suite / CI), steering it toward human-in-the-loop.
//
//	autonomous:
//	  discipline_gate:
//	    enabled: true
//	    min_score: 30      # 0–100; repos below this are gated
//	    mode: block        # block | warn
type DisciplineGateConfig struct {
	// Enabled gates the check. Pointer so explicit false ≠ unset (default true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// MinScore is the readiness floor (0–100). 0/unset → DefaultDisciplineMinScore.
	MinScore int `yaml:"min_score,omitempty" json:"minScore,omitempty"`
	// Mode is "block" (refuse autonomy below min_score) or "warn" (log only).
	// Empty → DefaultDisciplineGateMode.
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

const (
	// DefaultDisciplineGateEnabled — on by default (#4100).
	DefaultDisciplineGateEnabled = true
	// DefaultDisciplineMinScore — conservative: only repos lacking BOTH a real
	// test suite AND CI (score < 30) are gated; a repo with either clears it.
	DefaultDisciplineMinScore = 30
	// DefaultDisciplineGateMode — "block" refuses autonomy below the floor.
	DefaultDisciplineGateMode = "block"
)

// ResolveDisciplineGate returns the effective (enabled, minScore, mode).
func (a *AutonomousConfig) ResolveDisciplineGate() (enabled bool, minScore int, mode string) {
	enabled, minScore, mode = DefaultDisciplineGateEnabled, DefaultDisciplineMinScore, DefaultDisciplineGateMode
	if a == nil || a.DisciplineGate == nil {
		return
	}
	g := a.DisciplineGate
	if g.Enabled != nil {
		enabled = *g.Enabled
	}
	if g.MinScore > 0 {
		minScore = g.MinScore
	}
	if g.Mode == "warn" || g.Mode == "block" {
		mode = g.Mode
	}
	return
}

// YAMLDuration is a time.Duration that unmarshals from human-readable strings
// like "30s", "5m", "1h" in YAML (gopkg.in/yaml.v3 cannot do this natively).
type YAMLDuration time.Duration

func (d *YAMLDuration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		// Try as raw number (nanoseconds)
		var n int64
		if err2 := value.Decode(&n); err2 != nil {
			return fmt.Errorf("cannot parse duration: %w", err)
		}
		*d = YAMLDuration(time.Duration(n))
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = YAMLDuration(dur)
	return nil
}

// Duration returns the underlying time.Duration.
func (d YAMLDuration) Duration() time.Duration {
	return time.Duration(d)
}

// RepositoryConfig holds per-repository autonomous scheduler settings.
type RepositoryConfig struct {
	// Forge is the forge ID (key in the forges: block) this repo is hosted on.
	// When empty, the workspace default forge is used. Matches Router.MapRepo semantics.
	Forge string `yaml:"forge,omitempty" json:"forge,omitempty"`
}

// AutonomousConfig holds autonomous scheduler settings from config.yaml.
type AutonomousConfig struct {
	// ScanInterval is the duration between board scans. Default: 30s.
	ScanInterval YAMLDuration `yaml:"scan_interval" json:"scanInterval,omitempty"`
	// BudgetCeiling is the global token budget ceiling. 0 = unlimited.
	BudgetCeiling int64 `yaml:"budget_ceiling" json:"budgetCeiling,omitempty"`
	// DebounceRepos only re-queries repos with recent completions. Default: true.
	DebounceRepos *bool `yaml:"debounce_repos" json:"debounceRepos,omitempty"`
	// DryRun shows what would run without executing. Default: false.
	DryRun *bool `yaml:"dry_run" json:"dryRun,omitempty"`
	// PickupBacklog controls whether the autonomous scheduler dispatches issues
	// in "Backlog" status after all "Ready" items for a repo have been processed.
	// Default: false — only "Ready" items are dispatched.
	// Set to true for repos where issues are created directly into a pipeline-
	// ready state (no manual triage step). Ready items always take priority
	// regardless of this setting.
	PickupBacklog *bool `yaml:"pickup_backlog" json:"pickupBacklog,omitempty"`
	// SafetyRails holds safety rail overrides. Nil = use defaults.
	SafetyRails *SafetyRailsConfig `yaml:"safety_rails,omitempty" json:"safetyRails,omitempty"`
	// DisciplineGate down-ranks/refuses full autonomy on an under-prepared repo
	// (#4100): a repo with no real test suite / CI is where autonomous gates
	// over-trust themselves. Nil = use defaults (enabled, block below 30).
	DisciplineGate *DisciplineGateConfig `yaml:"discipline_gate,omitempty" json:"disciplineGate,omitempty"`
	// AutoActionable controls whether auto-refined issues are placed directly
	// into Ready status (true) or held in Backlog for manual review (false).
	// Default: false — issues require manual triage before becoming actionable.
	AutoActionable *bool `yaml:"auto_actionable" json:"autoActionable,omitempty"`
	// RefinementEnabled controls whether the autonomous refinement scheduler is
	// active. Default: true.
	RefinementEnabled *bool `yaml:"refinement_enabled" json:"refinementEnabled,omitempty"`
	// RefinementInterval is the time between refinement scan cycles.
	// Minimum: 30s (to prevent GitHub API rate-limit abuse). Default: 60s.
	RefinementInterval YAMLDuration `yaml:"refinement_interval" json:"refinementInterval,omitempty"`
	// RefinementMaxConcurrent is the maximum number of concurrent refinement
	// operations. Range: 1–3 (capped to prevent resource exhaustion). Default: 1.
	RefinementMaxConcurrent int `yaml:"refinement_max_concurrent" json:"refinementMaxConcurrent,omitempty"`
	// StallEscalationEnabled enables progressive stall escalation in autonomous
	// mode (Issue #2656). When true, autonomous pipelines escalate through 5
	// levels instead of silently killing. Default: true.
	StallEscalationEnabled *bool `yaml:"stall_escalation_enabled" json:"stallEscalationEnabled,omitempty"`

	// StallPauseTimeout is the auto-abort timeout for the pause dialog in
	// autonomous mode (Issue #2656). Default: 30m.
	StallPauseTimeout YAMLDuration `yaml:"stall_pause_timeout" json:"stallPauseTimeout,omitempty"`

	// StallDetectionMinutes defines how long an issue may remain in
	// "In Progress" with a green, mergeable PR before the watchdog alerts.
	// Default: 60 minutes.
	StallDetectionMinutes int `yaml:"stall_detection_minutes" json:"stallDetectionMinutes,omitempty"`

	// AutoRedispatchStalled re-runs `pr merge` automatically when a stalled
	// ready-to-merge PR is detected. Default: false.
	AutoRedispatchStalled *bool `yaml:"auto_redispatch_stalled" json:"autoRedispatchStalled,omitempty"`

	// OnFailureStatus controls where issues move on the project board when a
	// pipeline run fails. Valid values: "ready" (default), "backlog", "unchanged".
	// "ready" allows the autonomous scheduler to re-dispatch on the next scan.
	// "unchanged" leaves the issue stuck in "In Progress" (legacy behavior).
	OnFailureStatus string `yaml:"on_failure_status" json:"onFailureStatus,omitempty"`

	// EnabledRepos restricts which repos the autonomous scheduler scans. When
	// non-empty, only these repos are queried on each scan cycle — cutting
	// GitHub GraphQL usage proportionally and letting users focus autonomous
	// mode on a specific repo (e.g., ["acme-platform"]).
	//
	// Values may be short names ("acme-platform") or fully-qualified
	// ("acme/platform"). Short names are resolved against
	// the configured owner.
	//
	// Empty/unset = scan all configured repos (current default behavior).
	EnabledRepos []string `yaml:"enabled_repos,omitempty" json:"enabledRepos,omitempty"`

	// DisableEpicBlockedByCascade disables the default behaviour where a
	// sub-issue is treated as blocked when its parent epic has an open
	// blockedBy dependency. When false (default), the cascade is active and
	// sub-issues are gated by their epic's blockers. Set to true to revert to
	// individual-issue-only blocking.
	DisableEpicBlockedByCascade bool `yaml:"disable_epic_blockedby_cascade" json:"disableEpicBlockedByCascade,omitempty"`

	// Repositories holds per-repository scheduler settings. Keys may be short
	// names ("my-repo") or fully-qualified ("nightgauge/my-repo"). Short names are
	// resolved against the configured Owner.
	Repositories map[string]*RepositoryConfig `yaml:"repositories,omitempty" json:"repositories,omitempty"`

	// StuckEpicDetection configures the no-silent-stall watchdog (#4073). Nil =
	// defaults (enabled, 6h re-alert cooldown, webhook from
	// NIGHTGAUGE_STUCK_EPIC_WEBHOOK).
	StuckEpicDetection *StuckEpicDetectionConfig `yaml:"stuck_epic_detection,omitempty" json:"stuckEpicDetection,omitempty"`

	// ExcludeLabels lists human-only labels that must never be dispatched by
	// the autonomous scheduler or enqueued by epic expansion (#317). An issue
	// carrying one of these labels needs a human operator action (e.g.
	// rotating a cloud credential) that no amount of code changes can
	// satisfy — dispatching it burns tokens through
	// issue-pickup → planning → feature-dev → validate and then fails at
	// pr-create with nothing to commit. Matched case-insensitively against
	// each candidate's labels. Empty/unset resolves to the single default
	// ["owner-action"] via ResolvedExcludeLabels — there is no separate
	// on/off knob, only this one list.
	ExcludeLabels []string `yaml:"exclude_labels,omitempty" json:"excludeLabels,omitempty"`
}

// StuckEpicDetectionConfig configures the no-silent-stall epic watchdog (#4073).
// When an epic is open with open sub-issues but has zero eligible work, no
// running pipeline, and no sub-issue actively recovering, the autonomous
// scheduler surfaces it as stalled instead of letting it look "done".
type StuckEpicDetectionConfig struct {
	// Enabled gates the watchdog. Nil/unset = true.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// DiscordWebhookEnv names the environment variable holding the Discord
	// webhook URL for stalled-epic alerts. Empty = NIGHTGAUGE_STUCK_EPIC_WEBHOOK.
	// The secret stays in the environment, never in config.yaml.
	DiscordWebhookEnv string `yaml:"discord_webhook_env,omitempty" json:"discordWebhookEnv,omitempty"`
	// ReAlertAfter is the cooldown before re-alerting on the same still-stalled
	// epic. 0/unset = 6h.
	ReAlertAfter YAMLDuration `yaml:"re_alert_after,omitempty" json:"reAlertAfter,omitempty"`
}

// DefaultStuckEpicWebhookEnv is the env var consulted for the stalled-epic
// Discord webhook URL when none is configured.
const DefaultStuckEpicWebhookEnv = "NIGHTGAUGE_STUCK_EPIC_WEBHOOK"

// ReadyToShipConfig configures the post-epic "ready to ship" notification
// (#4076). When an epic fully closes (all sub-issues merged and the epic
// auto-closed), the pipeline emits a Discord message carrying the exact deploy
// dispatch command — a bridge from "epic closed" to "ready to ship" that NEVER
// auto-submits to stores (it only notifies; a human runs the command).
type ReadyToShipConfig struct {
	// Enabled gates the notification. Nil/unset = true.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// DiscordWebhookEnv names the environment variable holding the Discord
	// webhook URL. Empty = NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK. The secret stays
	// in the environment, never in config.yaml.
	DiscordWebhookEnv string `yaml:"discord_webhook_env,omitempty" json:"discordWebhookEnv,omitempty"`
	// DeployCommand is the dispatch command surfaced in the alert. Empty =
	// DefaultDeployCommand. It is only DISPLAYED — never executed.
	DeployCommand string `yaml:"deploy_command,omitempty" json:"deployCommand,omitempty"`
}

// DefaultShipNotifyWebhookEnv is the env var consulted for the ready-to-ship
// Discord webhook URL when none is configured.
const DefaultShipNotifyWebhookEnv = "NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK"

// DefaultDeployCommand is the dispatch command surfaced in the ready-to-ship
// notification when none is configured. It is only displayed, never run.
const DefaultDeployCommand = "gh workflow run deploy-stores.yml -f platforms=all"

// ValidateAutonomousConfig checks autonomous config constraints that cannot be
// expressed as struct types alone. Returns an error if any constraint is violated.
func ValidateAutonomousConfig(a *AutonomousConfig) error {
	if a == nil {
		return nil
	}
	const minRefinementInterval = 30 * time.Second
	if a.RefinementInterval > 0 && time.Duration(a.RefinementInterval) < minRefinementInterval {
		return fmt.Errorf("autonomous.refinement_interval must be >= 30s (got %s); minimum enforced to prevent GitHub API rate-limit abuse", time.Duration(a.RefinementInterval))
	}
	if a.RefinementMaxConcurrent != 0 && (a.RefinementMaxConcurrent < 1 || a.RefinementMaxConcurrent > 3) {
		return fmt.Errorf("autonomous.refinement_max_concurrent must be in range [1, 3] (got %d); capped to prevent resource exhaustion", a.RefinementMaxConcurrent)
	}
	switch a.OnFailureStatus {
	case "", "ready", "backlog", "unchanged":
		// valid
	default:
		return fmt.Errorf("autonomous.on_failure_status must be one of: ready, backlog, unchanged (got %q)", a.OnFailureStatus)
	}
	return nil
}

// ResolvedOnFailureStatus returns the effective on_failure_status value,
// defaulting to "ready" when unset.
func (a *AutonomousConfig) ResolvedOnFailureStatus() string {
	if a == nil || a.OnFailureStatus == "" {
		return "ready"
	}
	return a.OnFailureStatus
}

// ResolvedEnabledRepos returns EnabledRepos normalized to "owner/repo" form.
// Short names ("acme-platform") are expanded using the provided
// defaultOwner. Fully-qualified entries ("nightgauge/foo") pass through unchanged.
// Whitespace is trimmed and empty entries dropped. Returns nil when unset.
func (a *AutonomousConfig) ResolvedEnabledRepos(defaultOwner string) []string {
	if a == nil || len(a.EnabledRepos) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.EnabledRepos))
	for _, r := range a.EnabledRepos {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.Contains(r, "/") {
			out = append(out, r)
			continue
		}
		if defaultOwner == "" {
			// No owner to expand against — keep the short name and let
			// downstream matching (which is case-insensitive full-name) skip it.
			out = append(out, r)
			continue
		}
		out = append(out, defaultOwner+"/"+r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DefaultExcludeLabels is the single default human-only label the autonomous
// scheduler and epic-enqueue paths refuse to dispatch when
// autonomous.exclude_labels is unset (#317).
var DefaultExcludeLabels = []string{"owner-action"}

// ResolvedExcludeLabels returns the effective set of human-only labels that
// must never be dispatched, trimmed and with empty entries dropped. Falls
// back to DefaultExcludeLabels when unset — a single resolved option, not a
// separate enable/disable knob.
func (a *AutonomousConfig) ResolvedExcludeLabels() []string {
	if a == nil || len(a.ExcludeLabels) == 0 {
		return DefaultExcludeLabels
	}
	out := make([]string, 0, len(a.ExcludeLabels))
	for _, l := range a.ExcludeLabels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return DefaultExcludeLabels
	}
	return out
}

// IsStallEscalationEnabled returns whether autonomous stall escalation is
// enabled. Default: true.
func (a *AutonomousConfig) IsStallEscalationEnabled() bool {
	if a == nil || a.StallEscalationEnabled == nil {
		return true
	}
	return *a.StallEscalationEnabled
}

// ResolvedStallPauseTimeout returns the auto-abort timeout for stall pause.
// Default: 30 minutes.
func (a *AutonomousConfig) ResolvedStallPauseTimeout() time.Duration {
	if a == nil || a.StallPauseTimeout == 0 {
		return 30 * time.Minute
	}
	return time.Duration(a.StallPauseTimeout)
}

// ResolvedStallDetectionMinutes returns the watchdog threshold for stalled
// in-progress issues with passing PR checks. Default: 60 minutes.
func (a *AutonomousConfig) ResolvedStallDetectionMinutes() int {
	if a == nil || a.StallDetectionMinutes <= 0 {
		return 60
	}
	return a.StallDetectionMinutes
}

// IsAutoRedispatchStalled returns whether the autonomous watchdog should
// auto-run `pr merge` for stalled ready-to-merge PRs. Default: false.
func (a *AutonomousConfig) IsAutoRedispatchStalled() bool {
	if a == nil || a.AutoRedispatchStalled == nil {
		return false
	}
	return *a.AutoRedispatchStalled
}

// DefaultAutonomousConfig returns default autonomous scheduler values.
func DefaultAutonomousConfig() *AutonomousConfig {
	autoActionable := false
	refinementEnabled := true
	stallEscalation := true
	autoRedispatchStalled := false
	return &AutonomousConfig{
		ScanInterval:            YAMLDuration(30 * time.Second),
		BudgetCeiling:           0,
		AutoActionable:          &autoActionable,
		RefinementEnabled:       &refinementEnabled,
		StallEscalationEnabled:  &stallEscalation,
		StallPauseTimeout:       YAMLDuration(30 * time.Minute),
		StallDetectionMinutes:   60,
		AutoRedispatchStalled:   &autoRedispatchStalled,
		RefinementInterval:      YAMLDuration(60 * time.Second),
		RefinementMaxConcurrent: 1,
	}
}

// Config holds CLI configuration.
// RoutingConfig models the routing: section of config.yaml. It mirrors
// RoutingConfigSchema in packages/nightgauge-vscode/src/config/schema.ts:
// complexity thresholds plus the customizable change_rules fast-track table
// (#4125). All fields are optional so existing configs keep working and the
// built-in defaults apply with zero user config.
type RoutingConfig struct {
	// TrivialMaxComplexity / ExtensiveMinComplexity override the complexity
	// thresholds. Pointers so "unset" is distinguishable from 0.
	TrivialMaxComplexity   *int `json:"trivialMaxComplexity,omitempty" yaml:"trivial_max_complexity,omitempty"`
	ExtensiveMinComplexity *int `json:"extensiveMinComplexity,omitempty" yaml:"extensive_min_complexity,omitempty"`
	// ForceFullPipeline disables all stage skipping and change_rules overrides.
	ForceFullPipeline bool `json:"forceFullPipeline,omitempty" yaml:"force_full_pipeline,omitempty"`
	// ChangeRules is the user-customizable fast-track table. Empty/omitted means
	// the built-in routing.DefaultChangeRules() apply. The element type is the
	// single Go source of truth shared with routing.Derive().
	ChangeRules []routing.ChangeRule `json:"changeRules,omitempty" yaml:"change_rules,omitempty"`
}

// ModelRoutingConfig mirrors the model_routing: section of config.yaml — the
// TS ModelRoutingConfigSchema in packages/nightgauge-vscode/src/config/schema.ts.
// The Go autonomous scheduler consumes only minimum_model (the per-stage model
// floor); mode/effort_auto/confidence_threshold are TS model-selector concerns
// that the deterministic dispatch path does not read, so they are omitted here
// per the "consume only what you need" rule.
type ModelRoutingConfig struct {
	// MinimumModel is the per-stage model floor: stage name → tier
	// (haiku < sonnet < opus < fable). A stage whose predicted model resolves
	// below its floor is raised to the floor at dispatch (#366), giving the Go
	// autonomous path parity with the TS SkillRunner's enforceMinimumModel.
	// Setting a stage floor of "fable" opts that stage into the premium
	// frontier tier.
	MinimumModel map[string]string `json:"minimumModel,omitempty" yaml:"minimum_model,omitempty"`
}

type Config struct {
	// GitHub settings
	Owner         string         `json:"owner"`
	OwnerType     string         `json:"ownerType"` // "org" (default) or "user"
	ProjectNumber int            `json:"projectNumber"`
	Projects      []ProjectEntry `json:"projects,omitempty" yaml:"projects,omitempty"`
	DefaultRepo   string         `json:"defaultRepo"`

	// GitHub user identity (per-repo mapping for multi-account workspaces)
	GitHubUser string            `json:"githubUser,omitempty" yaml:"github_user,omitempty"`
	GitHubAuth *GitHubAuthConfig `json:"githubAuth,omitempty" yaml:"github_auth,omitempty"`

	// Platform settings
	PlatformEnabled *bool  `json:"platformEnabled,omitempty"`
	PlatformURL     string `json:"platformUrl,omitempty"`
	APIKey          string `json:"apiKey,omitempty"`
	LicenseKey      string `json:"licenseKey,omitempty"`

	// Binary settings
	LogLevel string `json:"logLevel"`

	// Sanitization settings
	Sanitization *SanitizationConfig `json:"sanitization,omitempty" yaml:"sanitization,omitempty"`

	// Feedback loop / health monitoring settings
	FeedbackLoop *FeedbackLoopConfig `json:"feedbackLoop,omitempty" yaml:"feedback_loop,omitempty"`

	// Telemetry settings
	Telemetry *TelemetryConfig `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`

	// Remote command polling settings
	RemoteCommands *RemoteCommandsConfig `json:"remoteCommands,omitempty" yaml:"remote_commands,omitempty"`

	// Agent teams scaling settings
	AgentTeams *AgentTeamsConfig `json:"agentTeams,omitempty" yaml:"agent_teams,omitempty"`

	// Autonomous scheduler settings
	Autonomous *AutonomousConfig `json:"autonomous,omitempty" yaml:"autonomous,omitempty"`

	// ReadyToShip configures the post-epic "ready to ship" notification (#4076).
	ReadyToShip *ReadyToShipConfig `json:"readyToShip,omitempty" yaml:"ready_to_ship,omitempty"`

	// SizeToEstimate maps size label values (lowercase) to story-point estimates.
	// Configurable under project.size_to_estimate in config.yaml.
	// Default: XS=1, S=2, M=3, L=5, XL=8.
	SizeToEstimate map[string]float64 `json:"sizeToEstimate,omitempty" yaml:"size_to_estimate,omitempty"`

	// Knowledge holds knowledge base settings from the knowledge: section.
	Knowledge *KnowledgeConfig `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`

	// PipelineExecutor controls which execution substrate runs pipeline stages.
	// Configured via pipeline.executor in config.yaml.
	PipelineExecutor *PipelineExecutorConfig `json:"pipelineExecutor,omitempty" yaml:"pipeline_executor,omitempty"`

	// Pipeline holds settings from the pipeline: section of config.yaml.
	// Currently exposes max_concurrent — the unified slot ceiling that
	// controls both the TS-side ConcurrentPipelineManager and the Go-side
	// autonomous scheduler. Kept as a pointer so callers can detect "unset"
	// and fall back to legacy autonomous.max_concurrent if needed.
	Pipeline *PipelineConfig `json:"pipeline,omitempty" yaml:"pipeline,omitempty"`

	// Routing holds the routing: section of config.yaml — complexity thresholds
	// plus the customizable change_rules fast-track table (#4125). Read by the
	// Go scheduler/CLI to feed routing.Derive(); mirrors RoutingConfigSchema in
	// packages/nightgauge-vscode/src/config/schema.ts.
	Routing *RoutingConfig `json:"routing,omitempty" yaml:"routing,omitempty"`

	// ModelRouting holds the subset of the model_routing: section the Go
	// scheduler reads — the per-stage minimum_model floor (#366). Mirrors
	// ModelRoutingConfigSchema.minimum_model in
	// packages/nightgauge-vscode/src/config/schema.ts.
	ModelRouting *ModelRoutingConfig `json:"modelRouting,omitempty" yaml:"model_routing,omitempty"`

	// UI holds the subset of the ui: section the Go binary reads — today only
	// ui.core.adapter, the global execution-adapter default in the canonical
	// per-stage adapter schema (#54). Mirrors UICoreConfigSchema.adapter in
	// packages/nightgauge-vscode/src/config/schema.ts.
	UI *UIConfig `json:"ui,omitempty" yaml:"ui,omitempty"`

	// Concurrency is the single source of truth for how many pipelines run at
	// once, across the workspace and per repository. Owned by the machine tier
	// (~/.nightgauge/config.yaml). See docs/SETTINGS_ARCHITECTURE.md.
	Concurrency *ConcurrencyConfig `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`

	// SchemaVersion is the config file format version ("1" or "2"). Missing or
	// empty version is treated as v1 and triggers automatic in-memory v1→v2
	// migration (inserting a default forges.github block). The file on disk is
	// never rewritten during migration — only the in-memory Config is updated.
	SchemaVersion string `json:"schemaVersion,omitempty" yaml:"schema_version,omitempty"`

	// Forges holds optional per-forge configuration consumed by the
	// `nightgauge forge` subcommand. The map key is the forge id
	// (e.g. "github", "acme-gitlab"); the value carries adapter Kind
	// plus owner/project/token-env metadata. When the map is absent or
	// empty, `forge` falls back to the singleton GitHub adapter built
	// from the legacy top-level fields.
	Forges map[string]*ForgeConfigEntry `json:"forges,omitempty" yaml:"forges,omitempty"`

	// Notifications holds inbound webhook receiver settings (e.g. Mattermost
	// outgoing-webhook handler). Disabled by default — the receiver is only
	// started when notifications.inbound.enabled is true.
	Notifications *NotificationsConfig `json:"notifications,omitempty" yaml:"notifications,omitempty"`

	// Notifiers holds per-channel signing tokens for inbound providers
	// (Mattermost today, Slack later). Tokens use env:VAR_NAME refs to
	// avoid plaintext secrets in YAML — the receiver resolves them at load
	// time.
	Notifiers *NotifiersConfig `json:"notifiers,omitempty" yaml:"notifiers,omitempty"`

	// Users maps Mattermost user IDs to GitHub/GitLab identities for
	// per-user authorization of inbound slash commands. Uses mattermost_user_id
	// (stable identifier) rather than user_name (which can change).
	Users []UserMappingEntry `json:"users,omitempty" yaml:"users,omitempty"`
}

// ProjectEntry is one selectable GitHub Projects V2 board. When Projects is
// non-empty it is authoritative over the legacy single ProjectNumber field.
type ProjectEntry struct {
	Name       string `json:"name" yaml:"name"`
	Number     int    `json:"number" yaml:"number"`
	SyncFilter string `json:"syncFilter,omitempty" yaml:"sync_filter,omitempty"`
	Default    bool   `json:"default,omitempty" yaml:"default,omitempty"`
}

// UserMappingEntry maps a Mattermost user ID to a GitHub and/or GitLab identity.
// Used by the inbound authorization layer to verify that a Mattermost command sender
// has the required GitHub/GitLab permissions before dispatching pipeline operations.
type UserMappingEntry struct {
	MattermostUserID string `yaml:"mattermost_user_id" json:"mattermost_user_id"`
	GitHubLogin      string `yaml:"github_login,omitempty" json:"github_login,omitempty"`
	GitLabUsername   string `yaml:"gitlab_username,omitempty" json:"gitlab_username,omitempty"`
}

// NotificationsConfig is the top-level notifications: block. Today only
// the inbound receiver is configurable here; outbound notifier settings
// (Discord, Slack, Mattermost) live in the TS extension config.
type NotificationsConfig struct {
	Inbound *InboundConfig `json:"inbound,omitempty" yaml:"inbound,omitempty"`
}

// InboundConfig configures the in-binary HTTP receiver that accepts
// Mattermost outgoing-webhook callbacks. Defaults are loopback-only
// plaintext: 127.0.0.1:8765/mattermost. TLS is expected to terminate
// at a reverse proxy — see docs/MATTERMOST_INBOUND.md.
type InboundConfig struct {
	Enabled bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Host    string `json:"host,omitempty" yaml:"host,omitempty"`
	Port    int    `json:"port,omitempty" yaml:"port,omitempty"`
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
	// GitLab configures the GitLab project-hook webhook receiver.
	// When nil or Enabled=false the receiver is not started.
	GitLab *GitLabInboundConfig `json:"gitlab,omitempty" yaml:"gitlab,omitempty"`
}

// GitLabInboundConfig configures the GitLab webhook receiver. Defaults bind
// to loopback on port 8766 (Mattermost uses 8765) with path /gitlab.
type GitLabInboundConfig struct {
	Enabled         bool   `json:"enabled,omitempty"          yaml:"enabled"`
	Host            string `json:"host,omitempty"             yaml:"host"`               // default: 127.0.0.1
	Port            int    `json:"port,omitempty"             yaml:"port"`               // default: 8766
	Path            string `json:"path,omitempty"             yaml:"path"`               // default: /gitlab
	SecretEnvVar    string `json:"secret_env_var,omitempty"   yaml:"secret_env_var"`     // env var holding shared secret
	ReplayWindowSec int    `json:"replay_window_sec,omitempty" yaml:"replay_window_sec"` // default: 300
	DedupeWindowSec int    `json:"dedupe_window_sec,omitempty" yaml:"dedupe_window_sec"` // default: 3600
	DedupeDBPath    string `json:"dedupe_db_path,omitempty"   yaml:"dedupe_db_path"`     // default: ":memory:"
	MetricsEnabled  bool   `json:"metrics_enabled,omitempty"  yaml:"metrics_enabled"`    // default: true
}

// DefaultGitLabInboundPort is the default port for the GitLab webhook receiver.
// 8766 avoids conflicts with the Mattermost receiver on 8765.
const DefaultGitLabInboundPort = 8766

// ResolvedHost returns the configured host or the loopback default.
func (c *GitLabInboundConfig) ResolvedHost() string {
	if c == nil || c.Host == "" {
		return "127.0.0.1"
	}
	return c.Host
}

// ResolvedPort returns the configured port or the documented default 8766.
func (c *GitLabInboundConfig) ResolvedPort() int {
	if c == nil || c.Port == 0 {
		return DefaultGitLabInboundPort
	}
	return c.Port
}

// ResolvedPath returns the configured path or the documented default /gitlab.
func (c *GitLabInboundConfig) ResolvedPath() string {
	p := ""
	if c != nil {
		p = c.Path
	}
	if p == "" {
		return "/gitlab"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// ResolvedReplayWindowSec returns the configured replay window in seconds or
// the default of 300 (5 minutes).
func (c *GitLabInboundConfig) ResolvedReplayWindowSec() int {
	if c == nil || c.ReplayWindowSec == 0 {
		return 300
	}
	return c.ReplayWindowSec
}

// ResolvedDedupeWindowSec returns the configured dedup window in seconds or
// the default of 3600 (1 hour).
func (c *GitLabInboundConfig) ResolvedDedupeWindowSec() int {
	if c == nil || c.DedupeWindowSec == 0 {
		return 3600
	}
	return c.DedupeWindowSec
}

// ResolveSecret reads the shared secret from the environment variable named by
// SecretEnvVar. Returns ("", error) when the variable is unset or empty.
func (c *GitLabInboundConfig) ResolveSecret() (string, error) {
	if c == nil || c.SecretEnvVar == "" {
		return "", fmt.Errorf("gitlab inbound: secret_env_var is required")
	}
	val := os.Getenv(c.SecretEnvVar)
	if val == "" {
		return "", fmt.Errorf("environment variable %q referenced by gitlab secret_env_var is not set or empty", c.SecretEnvVar)
	}
	return val, nil
}

// ResolvedHost returns the configured host or the loopback default.
func (c *InboundConfig) ResolvedHost() string {
	if c == nil || c.Host == "" {
		return "127.0.0.1"
	}
	return c.Host
}

// DefaultInboundPort is the documented default for the Mattermost inbound
// webhook receiver. It is applied at the cmd/ wiring layer rather than in
// a getter so that Port=0 retains the standard "OS-assigns" semantic for
// tests that bind to an ephemeral port.
const DefaultInboundPort = 8765

// ResolvedPath returns the configured path or the documented default
// /mattermost. Always begins with a leading slash.
func (c *InboundConfig) ResolvedPath() string {
	p := ""
	if c != nil {
		p = c.Path
	}
	if p == "" {
		return "/mattermost"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// NotifiersConfig groups per-provider notifier settings. Today only
// Mattermost is wired; Slack and other providers slot in as siblings.
type NotifiersConfig struct {
	Mattermost *MattermostNotifierConfig `json:"mattermost,omitempty" yaml:"mattermost,omitempty"`
}

// MattermostNotifierConfig holds per-channel signing tokens for the
// inbound webhook receiver. Channel name maps to the env-referenced
// signing token Mattermost includes in the `token` form field.
type MattermostNotifierConfig struct {
	Channels map[string]*ChannelToken `json:"channels,omitempty" yaml:"channels,omitempty"`
}

// ChannelToken describes the signing token for a single Mattermost
// channel. TokenEnv is the env-var name from which the token is read
// (matches the env:VAR_NAME convention used elsewhere, but does not
// require the env: prefix here — the field name itself signals the
// indirection).
type ChannelToken struct {
	TokenEnv string `json:"tokenEnv,omitempty" yaml:"token_env,omitempty"`
}

// ResolveToken returns the actual signing token by reading the
// referenced env var. Returns ("", error) when the env var is unset
// or empty. Unlike resolveEnvRef, this never accepts plaintext — a
// channel without an env-referenced token is a config error.
func (c *ChannelToken) ResolveToken() (string, error) {
	if c == nil || c.TokenEnv == "" {
		return "", fmt.Errorf("channel token: token_env is required")
	}
	val := os.Getenv(c.TokenEnv)
	if val == "" {
		return "", fmt.Errorf("environment variable %q referenced by channel token is not set or empty", c.TokenEnv)
	}
	return val, nil
}

// ForgeConfigEntry describes one entry in the optional `forges:` block.
// All fields are optional except Kind — when Kind is empty, the entry
// is silently skipped at load time. TokenEnv is the env-var name from
// which the token will be read (matches the env:VAR_NAME convention
// used elsewhere, but does not require the env: prefix).
//
// New v2 fields: BaseURL, GraphQLURL, AuthMethod, CABundle, DefaultProjectID, Proxy.
// Legacy fields (Host, Owner, ProjectNumber, OwnerType) are retained for
// backward compatibility. When both Host and BaseURL are set, BaseURL takes
// precedence as the v2 canonical field.
type ForgeConfigEntry struct {
	Kind string `yaml:"kind" json:"kind,omitempty"`
	// BaseURL is the v2 canonical base URL for the forge (e.g. https://github.com,
	// https://gitlab.example.com). Required for non-github forge kinds.
	BaseURL string `yaml:"base_url,omitempty" json:"baseUrl,omitempty"`
	// GraphQLURL is the forge GraphQL API endpoint. When empty, derived from BaseURL.
	GraphQLURL string `yaml:"graphql_url,omitempty" json:"graphqlUrl,omitempty"`
	// AuthMethod selects the authentication mechanism: "token", "app", or "pat".
	AuthMethod string `yaml:"auth_method,omitempty" json:"authMethod,omitempty"`
	// CABundle is the path to a PEM CA certificate bundle, resolved relative to
	// the config file directory (not CWD).
	CABundle string `yaml:"ca_bundle,omitempty" json:"caBundle,omitempty"`
	// DefaultProjectID is the default numeric project/group ID (GitLab-specific).
	DefaultProjectID int `yaml:"default_project_id,omitempty" json:"defaultProjectId,omitempty"`
	// Proxy is an http:// or https:// proxy URL. Falls back to HTTPS_PROXY when empty.
	Proxy string `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	// InsecureSkipTLS disables TLS certificate verification for self-signed instances.
	// Setting this to true emits a startup warning; use ca_bundle instead when possible.
	InsecureSkipTLS bool `yaml:"insecure_skip_tls,omitempty" json:"insecureSkipTls,omitempty"`
	// Legacy fields retained for backward compatibility.
	Host          string `yaml:"host,omitempty" json:"host,omitempty"`
	Owner         string `yaml:"owner,omitempty" json:"owner,omitempty"`
	ProjectNumber int    `yaml:"project_number,omitempty" json:"projectNumber,omitempty"`
	OwnerType     string `yaml:"owner_type,omitempty" json:"ownerType,omitempty"`
	TokenEnv      string `yaml:"token_env,omitempty" json:"tokenEnv,omitempty"`
}

// UIConfig is the subset of the ui: block the Go binary reads (#54).
type UIConfig struct {
	Core *UICoreConfig `yaml:"core,omitempty" json:"core,omitempty"`
}

// UICoreConfig carries ui.core.adapter — the global execution-adapter default.
type UICoreConfig struct {
	Adapter string `yaml:"adapter,omitempty" json:"adapter,omitempty"`
}

// PipelineConfig captures the subset of the YAML pipeline: block that the Go
// binary needs. Other fields (worktree_base, stage_cost_caps, etc.) are owned
// by the TypeScript side and intentionally not mirrored here.
type PipelineConfig struct {
	// StallIdleMs is the absolute idle-kill threshold in milliseconds. When set,
	// overrides the computed threshold×multiplier value. Mirrors the TypeScript
	// pipeline.stall_idle_ms field (Issue #3484).
	StallIdleMs YAMLDuration `yaml:"stall_idle_ms,omitempty" json:"stallIdleMs,omitempty"`

	// Recovery holds the pipeline.recovery: block — the auto-triage recovery
	// registry's tunables. The registry's per-run attempt cap is read directly
	// from disk by recovery.GetMaxAttemptsPerRun; this struct only models the
	// fields the typed Go config surfaces (currently conflict_recovery).
	Recovery *PipelineRecoveryConfig `yaml:"recovery,omitempty" json:"recovery,omitempty"`

	// AdversarialReview gates the feature-validate adversarial-review phase
	// (#4097): fresh-eyes LLM critics (correctness/security/reuse/tests) run as
	// a validate preflight and a "catch" trips the deterministic
	// FeatureValidateGate via gate-metrics. Default ON.
	AdversarialReview *AdversarialReviewConfig `yaml:"adversarial_review,omitempty" json:"adversarialReview,omitempty"`

	// GroundingGate gates the pre-feature-dev grounding check (#4099): confirms
	// the agent is on the issue's feature branch (not the base) with the issue
	// context present before feature-dev acts, closing the #3863 "am I on the
	// right issue/branch?" gap. Default ON.
	GroundingGate *GroundingGateConfig `yaml:"grounding_gate,omitempty" json:"groundingGate,omitempty"`

	// ArchitectureApproval gates feature-dev on a high-impact architectural
	// decision until a human approves it (#4098). A deterministic hard gate, so
	// it is exempt from human_in_the_loop.auto_accept_stages by construction.
	// Default ON.
	ArchitectureApproval *ArchitectureApprovalConfig `yaml:"architecture_approval,omitempty" json:"architectureApproval,omitempty"`

	// Gates holds per-stage post-condition gate tunables — currently the
	// trivial-change relaxation opt-in (#4128). Nil/absent means every gate runs
	// its full behavior (relaxation is strictly opt-in).
	Gates *PipelineGatesConfig `yaml:"gates,omitempty" json:"gates,omitempty"`

	// Survival holds the pipeline.survival: block — the post-merge survival
	// outcome model's window (#4151). Nil/absent means the default window applies.
	Survival *SurvivalConfig `yaml:"survival,omitempty" json:"survival,omitempty"`

	// TokenBudgetCeiling is the pipeline.token_budget_ceiling: block — the
	// per-pipeline USD ceiling used by budget-aware model escalation (#3542)
	// and TS-side ceiling enforcement. Modeled in the typed config so the
	// scheduler reads it through the tier merge (machine → project → local)
	// instead of hand-parsing the project file.
	TokenBudgetCeiling *TokenBudgetCeilingConfig `yaml:"token_budget_ceiling,omitempty" json:"tokenBudgetCeiling,omitempty"`

	// StageAdapters maps a pipeline stage to the execution adapter that runs
	// it — the canonical pipeline.stage_adapters.<stage> schema shared with
	// the VSCode resolver and the SDK CLI (#54). Values use the collapsed
	// adapter vocabulary (claude|codex|gemini|gemini-sdk|lm-studio|ollama|
	// copilot); the Go registry maps "claude" to its headless flavor.
	StageAdapters map[string]string `yaml:"stage_adapters,omitempty" json:"stageAdapters,omitempty"`

	// AdapterFallbackChain is the ordered pipeline.adapter_fallback_chain —
	// parsed by all three layers so the schema stays canonical (#54). The
	// health-aware fallback walker lives in the VSCode layer; the Go
	// scheduler surfaces resolution failures instead of walking.
	AdapterFallbackChain []string `yaml:"adapter_fallback_chain,omitempty" json:"adapterFallbackChain,omitempty"`
}

// TokenBudgetCeilingConfig is the pipeline.token_budget_ceiling: block.
//
//	pipeline:
//	  token_budget_ceiling:
//	    ceiling_usd: 75
type TokenBudgetCeilingConfig struct {
	// CeilingUSD is the per-pipeline budget ceiling in USD. 0/absent → default.
	CeilingUSD float64 `yaml:"ceiling_usd,omitempty" json:"ceilingUsd,omitempty"`
}

// SurvivalConfig is the pipeline.survival: block (#4151, spike #4134).
//
//	pipeline:
//	  survival:
//	    window_days: 7
//
// WindowDays is the post-merge observation window. A captured survival record
// stays pending until the window elapses; a revert/breakage observed at any
// point finalizes it negative immediately, and a record never re-observed by
// 2×window ages out to `unobserved` (no signal).
type SurvivalConfig struct {
	// WindowDays overrides the default observation window. 0/absent → default.
	WindowDays int `yaml:"window_days,omitempty" json:"windowDays,omitempty"`
}

// DefaultSurvivalWindowDays is the default post-merge observation window in days
// (#4151) — kept in sync with survival.DefaultWindowDays.
const DefaultSurvivalWindowDays = 7

// ResolveSurvivalWindowDays returns the effective survival observation window in
// days, applying the default in one place. Safe on a nil receiver.
func (p *PipelineConfig) ResolveSurvivalWindowDays() int {
	if p == nil || p.Survival == nil || p.Survival.WindowDays <= 0 {
		return DefaultSurvivalWindowDays
	}
	return p.Survival.WindowDays
}

// PipelineGatesConfig is the pipeline.gates: block. Each entry opts a specific
// post-condition gate into trivial-change relaxation (#4128).
type PipelineGatesConfig struct {
	PrCreate *GateRelaxConfig `yaml:"pr_create,omitempty" json:"prCreate,omitempty"`
	PrMerge  *GateRelaxConfig `yaml:"pr_merge,omitempty" json:"prMerge,omitempty"`
}

// GateRelaxConfig is one gate's relaxation opt-in (#4128).
//
//	pipeline:
//	  gates:
//	    pr_merge:
//	      relax_on_change_class: [docs_only, config_only]
//
// RelaxOnChangeClass lists the AUTHORITATIVE change classes (from the real
// post-dev diff) that relax this gate's retry/sleep overhead. Empty/absent =
// never relax (the safe default). A change whose real diff classifies outside
// the list runs the full gate — the classifier is the drift-revoke check.
type GateRelaxConfig struct {
	RelaxOnChangeClass []string `yaml:"relax_on_change_class,omitempty" json:"relaxOnChangeClass,omitempty"`
}

// RelaxClassesFor returns the relaxation change-class allowlist for the named
// gate ("pr-create" | "pr-merge"), or nil when unset. Safe on a nil receiver.
func (p *PipelineConfig) RelaxClassesFor(gate string) []string {
	if p == nil || p.Gates == nil {
		return nil
	}
	switch gate {
	case "pr-create":
		if p.Gates.PrCreate != nil {
			return p.Gates.PrCreate.RelaxOnChangeClass
		}
	case "pr-merge":
		if p.Gates.PrMerge != nil {
			return p.Gates.PrMerge.RelaxOnChangeClass
		}
	}
	return nil
}

// AdversarialReviewConfig is the pipeline.adversarial_review: block (#4097).
//
//	pipeline:
//	  adversarial_review:
//	    enabled: true
type AdversarialReviewConfig struct {
	// Enabled gates the adversarial-review phase. Pointer so an explicit
	// `false` is distinguishable from unset (which defaults to true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// DefaultAdversarialReviewEnabled is the default for
// pipeline.adversarial_review.enabled — the dormant critic is activated by
// default (#4097).
const DefaultAdversarialReviewEnabled = true

// ResolveAdversarialReviewEnabled returns the effective on/off value, applying
// the default in one place (single resolved value — no compat knobs).
func (p *PipelineConfig) ResolveAdversarialReviewEnabled() bool {
	if p == nil || p.AdversarialReview == nil || p.AdversarialReview.Enabled == nil {
		return DefaultAdversarialReviewEnabled
	}
	return *p.AdversarialReview.Enabled
}

// GroundingGateConfig is the pipeline.grounding_gate: block (#4099).
//
//	pipeline:
//	  grounding_gate:
//	    enabled: true
type GroundingGateConfig struct {
	// Enabled gates the pre-feature-dev grounding check. Pointer so an explicit
	// `false` is distinguishable from unset (which defaults to true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// DefaultGroundingGateEnabled is the default for
// pipeline.grounding_gate.enabled (#4099) — on by default.
const DefaultGroundingGateEnabled = true

// ArchitectureApprovalConfig is the pipeline.architecture_approval: block (#4098).
//
//	pipeline:
//	  architecture_approval:
//	    enabled: true
//	    approval_label: approved:architecture
type ArchitectureApprovalConfig struct {
	// Enabled gates the architecture-approval check. Pointer so an explicit
	// `false` is distinguishable from unset (which defaults to true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// ApprovalLabel is the issue label a human applies to grant approval.
	// Empty → DefaultArchitectureApprovalLabel.
	ApprovalLabel string `yaml:"approval_label,omitempty" json:"approvalLabel,omitempty"`
}

const (
	// DefaultArchitectureApprovalEnabled is the default for
	// pipeline.architecture_approval.enabled (#4098) — on by default.
	DefaultArchitectureApprovalEnabled = true
	// DefaultArchitectureApprovalLabel is the label a human applies to approve a
	// high-impact architectural decision.
	DefaultArchitectureApprovalLabel = "approved:architecture"
)

// ResolveArchitectureApprovalEnabled returns the effective on/off value.
func (p *PipelineConfig) ResolveArchitectureApprovalEnabled() bool {
	if p == nil || p.ArchitectureApproval == nil || p.ArchitectureApproval.Enabled == nil {
		return DefaultArchitectureApprovalEnabled
	}
	return *p.ArchitectureApproval.Enabled
}

// ResolveArchitectureApprovalLabel returns the effective approval label.
func (p *PipelineConfig) ResolveArchitectureApprovalLabel() string {
	if p == nil || p.ArchitectureApproval == nil || p.ArchitectureApproval.ApprovalLabel == "" {
		return DefaultArchitectureApprovalLabel
	}
	return p.ArchitectureApproval.ApprovalLabel
}

// ResolveGroundingGateEnabled returns the effective on/off value.
func (p *PipelineConfig) ResolveGroundingGateEnabled() bool {
	if p == nil || p.GroundingGate == nil || p.GroundingGate.Enabled == nil {
		return DefaultGroundingGateEnabled
	}
	return *p.GroundingGate.Enabled
}

// PipelineRecoveryConfig models the pipeline.recovery: block.
//
//	pipeline:
//	  recovery:
//	    conflict_recovery:
//	      enabled: true
//	      max_dev_redispatch: 2
type PipelineRecoveryConfig struct {
	// ConflictRecovery tunes the conflict-recovery loop (#4072): on an
	// unresolvable rebase conflict pr-merge re-dispatches feature-dev on the
	// same branch to resolve the conflict, bounded by max_dev_redispatch.
	ConflictRecovery *ConflictRecoveryConfig `yaml:"conflict_recovery,omitempty" json:"conflictRecovery,omitempty"`
}

// ConflictRecoveryConfig is the pipeline.recovery.conflict_recovery: block.
type ConflictRecoveryConfig struct {
	// Enabled gates the conflict-recovery loop. Pointer so an explicit `false`
	// is distinguishable from unset (which defaults to true).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// MaxDevRedispatch bounds how many times feature-dev is re-dispatched to
	// resolve a conflict before the loop escalates with the specific files.
	// 0/unset → DefaultConflictMaxDevRedispatch.
	MaxDevRedispatch int `yaml:"max_dev_redispatch,omitempty" json:"maxDevRedispatch,omitempty"`
}

const (
	// DefaultConflictRecoveryEnabled is the default for
	// pipeline.recovery.conflict_recovery.enabled.
	DefaultConflictRecoveryEnabled = true
	// DefaultConflictMaxDevRedispatch is the default bound on feature-dev
	// re-dispatch attempts. Lower than the legacy MaxConflictRestarts (3): a
	// dev re-dispatch is more expensive than a fresh restart, so 2 attempts
	// before escalating with the specific files is the resolved value.
	DefaultConflictMaxDevRedispatch = 2
)

// ResolvedConflictRecovery is the effective, defaults-applied conflict-recovery
// policy. Callers MUST use ResolveConflictRecovery so the default semantics
// stay in one place (single resolved value — no compat knobs).
type ResolvedConflictRecovery struct {
	Enabled          bool
	MaxDevRedispatch int
}

// ResolveConflictRecovery applies defaults to the configured
// pipeline.recovery.conflict_recovery block.
func ResolveConflictRecovery(cfg *Config) ResolvedConflictRecovery {
	out := ResolvedConflictRecovery{
		Enabled:          DefaultConflictRecoveryEnabled,
		MaxDevRedispatch: DefaultConflictMaxDevRedispatch,
	}
	if cfg == nil || cfg.Pipeline == nil || cfg.Pipeline.Recovery == nil {
		return out
	}
	cr := cfg.Pipeline.Recovery.ConflictRecovery
	if cr == nil {
		return out
	}
	if cr.Enabled != nil {
		out.Enabled = *cr.Enabled
	}
	if cr.MaxDevRedispatch > 0 {
		out.MaxDevRedispatch = cr.MaxDevRedispatch
	}
	return out
}

// ConcurrencyConfig is the single, canonical source of truth for pipeline
// concurrency. It replaces the prior tangle (pipeline.max_concurrent,
// autonomous.max_concurrent, autonomous.repositories.<repo>.sequential|
// max_concurrent). Owned by the machine tier so operator concurrency
// preferences are never committed to a repo.
//
//	concurrency:
//	  workspace_max: 3          # max issues running across ALL repos, combined
//	  per_repo_max: 1           # default max running within a SINGLE repo
//	  repository_overrides:     # optional per-repo override of per_repo_max
//	    acme-mobile: 2
type ConcurrencyConfig struct {
	// WorkspaceMax caps issues running concurrently across all repositories
	// combined. 0/unset → DefaultWorkspaceMax.
	WorkspaceMax int `yaml:"workspace_max,omitempty" json:"workspaceMax,omitempty"`
	// PerRepoMax is the default cap on issues running concurrently within a
	// single repository. 0/unset → DefaultPerRepoMax (1 = serialize per repo).
	PerRepoMax int `yaml:"per_repo_max,omitempty" json:"perRepoMax,omitempty"`
	// RepositoryOverrides overrides PerRepoMax for specific repositories, keyed
	// by short name ("acme-mobile") or "owner/repo".
	RepositoryOverrides map[string]int `yaml:"repository_overrides,omitempty" json:"repositoryOverrides,omitempty"`
}

// DefaultWorkspaceMax is the fallback combined ceiling when concurrency.
// workspace_max is unset.
const DefaultWorkspaceMax = 3

// DefaultPerRepoMax is the fallback per-repository cap when concurrency.
// per_repo_max is unset. 1 == serialize within a repo (the conflict-prone
// axis, since same-repo == same base branch).
const DefaultPerRepoMax = 1

// DefaultPipelineMaxConcurrent retained as an alias of DefaultWorkspaceMax for
// existing internal callers; new code should use DefaultWorkspaceMax.
const DefaultPipelineMaxConcurrent = DefaultWorkspaceMax

// ResolvedConcurrency is the effective, defaults-applied concurrency policy.
type ResolvedConcurrency struct {
	WorkspaceMax int
	PerRepoMax   int
	overrides    map[string]int
}

// CapForRepo returns the max concurrent pipelines allowed for a single repo:
// an explicit repository_overrides entry (by "owner/repo" then short name),
// else PerRepoMax.
func (r ResolvedConcurrency) CapForRepo(repo string) int {
	if r.overrides != nil {
		if v, ok := r.overrides[repo]; ok && v > 0 {
			return v
		}
		short := repo
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			short = repo[i+1:]
		}
		if v, ok := r.overrides[short]; ok && v > 0 {
			return v
		}
	}
	return r.PerRepoMax
}

// ResolveConcurrency applies defaults to the configured concurrency block.
// Callers MUST use this rather than reading ConcurrencyConfig fields directly
// so the default semantics stay in one place.
func ResolveConcurrency(cfg *Config) ResolvedConcurrency {
	out := ResolvedConcurrency{WorkspaceMax: DefaultWorkspaceMax, PerRepoMax: DefaultPerRepoMax}
	if cfg != nil && cfg.Concurrency != nil {
		c := cfg.Concurrency
		if c.WorkspaceMax > 0 {
			out.WorkspaceMax = c.WorkspaceMax
		}
		if c.PerRepoMax > 0 {
			out.PerRepoMax = c.PerRepoMax
		}
		out.overrides = c.RepositoryOverrides
	}
	return out
}

// ResolvedMaxConcurrent returns the effective workspace-wide concurrent-slot
// ceiling (max issues running across ALL repositories combined). Thin alias
// over ResolveConcurrency().WorkspaceMax, kept so existing callers that only
// need the global ceiling don't all churn.
func ResolvedMaxConcurrent(cfg *Config) int {
	return ResolveConcurrency(cfg).WorkspaceMax
}

// githubBlock is the legacy top-level `github:` block found in member configs
// generated for multi-repo workspaces. Only owner/repo participate in
// resolution; other keys (github_user, etc.) are ignored here because the
// canonical fields live elsewhere (Config.GitHubUser, Config.GitHubAuth).
type githubBlock struct {
	Owner string `yaml:"owner"`
	Repo  string `yaml:"repo"`
}

// yamlConfigNested is the current nested YAML format:
//
//	project:
//	  owner: nightgauge
//	  number: 1
//
// Also supports hybrid format where owner/repo live at top-level alongside a
// nested project block (see parseYAMLNested for fallback logic).
type yamlConfigNested struct {
	// SchemaVersion is the top-level schema_version field.
	SchemaVersion string `yaml:"schema_version,omitempty"`

	// Top-level owner/repo (hybrid format fallback)
	Owner     string `yaml:"owner"`
	OwnerType string `yaml:"owner_type"`
	Repo      string `yaml:"repo"`

	// GitHub holds the legacy top-level github: block. It is the LAST fallback
	// for owner/repo resolution — legacy member configs (e.g. generated for the
	// N:1 multi-repo workspace) carry only this block and no top-level repo:,
	// which otherwise resolves an empty DefaultRepo. See #3859.
	GitHub githubBlock `yaml:"github"`

	// GitHub user identity (per-repo and global fallback)
	GitHubUser string            `yaml:"github_user,omitempty"`
	GitHubAuth *GitHubAuthConfig `yaml:"github_auth,omitempty"`

	Project struct {
		Owner          string             `yaml:"owner"`
		OwnerType      string             `yaml:"owner_type"`
		Number         int                `yaml:"number"`
		Repo           string             `yaml:"repo"`
		SizeToEstimate map[string]float64 `yaml:"size_to_estimate,omitempty"`
	} `yaml:"project"`
	Projects     []ProjectEntry      `yaml:"projects,omitempty"`
	LogLevel     string              `yaml:"logLevel"`
	Sanitization *SanitizationConfig `yaml:"sanitization,omitempty"`
	FeedbackLoop *FeedbackLoopConfig `yaml:"feedback_loop,omitempty"`
	Platform     struct {
		// APIURL / LicenseKey mirror the VSCode extension's PlatformConfigSchema
		// (packages/nightgauge-vscode/src/config/schema.ts) — the schema the
		// extension actually writes to config.yaml. Until #333, these two keys
		// were silently dropped: only platform.telemetry was ever parsed, so
		// `nightgauge serve` (spawned by the extension with no flags/env) never
		// picked up a configured platform, leaving the Action Center bridge
		// (#330) and the remote-command poller permanently dormant.
		Enabled    *bool            `yaml:"enabled,omitempty"`
		APIURL     string           `yaml:"api_url,omitempty"`
		LicenseKey string           `yaml:"license_key,omitempty"`
		Telemetry  *TelemetryConfig `yaml:"telemetry,omitempty"`
	} `yaml:"platform,omitempty"`
	RemoteCommands   *RemoteCommandsConfig        `yaml:"remote_commands,omitempty"`
	AgentTeams       *AgentTeamsConfig            `yaml:"agent_teams,omitempty"`
	Autonomous       *AutonomousConfig            `yaml:"autonomous,omitempty"`
	ReadyToShip      *ReadyToShipConfig           `yaml:"ready_to_ship,omitempty"`
	SizeToEstimate   map[string]float64           `yaml:"size_to_estimate,omitempty"`
	Knowledge        *KnowledgeConfig             `yaml:"knowledge,omitempty"`
	PipelineExecutor *PipelineExecutorConfig      `yaml:"pipeline_executor,omitempty"`
	Pipeline         *PipelineConfig              `yaml:"pipeline,omitempty"`
	Routing          *RoutingConfig               `yaml:"routing,omitempty"`
	ModelRouting     *ModelRoutingConfig          `yaml:"model_routing,omitempty"`
	UI               *UIConfig                    `yaml:"ui,omitempty"`
	Forges           map[string]*ForgeConfigEntry `yaml:"forges,omitempty"`
	Notifications    *NotificationsConfig         `yaml:"notifications,omitempty"`
	Notifiers        *NotifiersConfig             `yaml:"notifiers,omitempty"`
}

// yamlConfigFlat is the legacy flat YAML format:
//
//	owner: nightgauge
//	project: 42
type yamlConfigFlat struct {
	SchemaVersion    string                       `yaml:"schema_version,omitempty"`
	Owner            string                       `yaml:"owner"`
	OwnerType        string                       `yaml:"owner_type"`
	GitHub           githubBlock                  `yaml:"github"`
	GitHubUser       string                       `yaml:"github_user,omitempty"`
	GitHubAuth       *GitHubAuthConfig            `yaml:"github_auth,omitempty"`
	Project          int                          `yaml:"project"` // legacy: project as bare integer
	ProjectNumber    int                          `yaml:"projectNumber"`
	Projects         []ProjectEntry               `yaml:"projects,omitempty"`
	DefaultRepo      string                       `yaml:"defaultRepo"`
	LogLevel         string                       `yaml:"logLevel"`
	Sanitization     *SanitizationConfig          `yaml:"sanitization,omitempty"`
	FeedbackLoop     *FeedbackLoopConfig          `yaml:"feedback_loop,omitempty"`
	Telemetry        *TelemetryConfig             `yaml:"telemetry,omitempty"`
	RemoteCommands   *RemoteCommandsConfig        `yaml:"remote_commands,omitempty"`
	AgentTeams       *AgentTeamsConfig            `yaml:"agent_teams,omitempty"`
	Autonomous       *AutonomousConfig            `yaml:"autonomous,omitempty"`
	ReadyToShip      *ReadyToShipConfig           `yaml:"ready_to_ship,omitempty"`
	SizeToEstimate   map[string]float64           `yaml:"size_to_estimate,omitempty"`
	Knowledge        *KnowledgeConfig             `yaml:"knowledge,omitempty"`
	PipelineExecutor *PipelineExecutorConfig      `yaml:"pipeline_executor,omitempty"`
	Pipeline         *PipelineConfig              `yaml:"pipeline,omitempty"`
	Routing          *RoutingConfig               `yaml:"routing,omitempty"`
	ModelRouting     *ModelRoutingConfig          `yaml:"model_routing,omitempty"`
	UI               *UIConfig                    `yaml:"ui,omitempty"`
	Forges           map[string]*ForgeConfigEntry `yaml:"forges,omitempty"`
	Notifications    *NotificationsConfig         `yaml:"notifications,omitempty"`
	Notifiers        *NotifiersConfig             `yaml:"notifiers,omitempty"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() *Config {
	return &Config{
		Owner:    "nightgauge",
		LogLevel: "info",
	}
}

// Load reads configuration for a workspace. A workspace requires a
// project-tier file (`.nightgauge/config.yaml`) to be meaningful —
// machine-tier alone identifies a developer's preferences but not which
// project to operate on. When the project file is present, Load merges
// machine + project + local tiers (later overrides earlier). When the
// project file is absent, Load falls back to `.nightgauge/config.json`
// (legacy JSON) before returning defaults.
//
// Tier files (when project YAML exists):
//   - ~/.nightgauge/config.yaml   — machine tier
//   - .nightgauge/config.yaml     — project tier
//   - .nightgauge/config.local.yaml — local tier (gitignored)
//
// When project YAML re-declares a Machine-classified key (see
// MachineTierKeys), Load emits a structured warning — the project value
// shadows the developer's machine setting, which is almost always a
// setup mistake. See docs/SETTINGS_ARCHITECTURE.md for the tier model.
func Load(workspaceRoot string) (*Config, error) {
	projectPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	if _, err := os.Stat(projectPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat project config: %w", err)
		}
		// No project YAML. Try legacy JSON before falling back to defaults.
		jsonPath := filepath.Join(workspaceRoot, ".nightgauge", "config.json")
		data, jerr := os.ReadFile(jsonPath)
		if jerr != nil {
			if os.IsNotExist(jerr) {
				return DefaultConfig(), nil
			}
			return nil, fmt.Errorf("read config: %w", jerr)
		}
		legacy := DefaultConfig()
		if err := json.Unmarshal(data, legacy); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		return legacy, nil
	}
	return LoadMerged(workspaceRoot)
}

// LoadYAML parses a YAML config file at path, returning a populated Config.
// It supports both nested (project.owner / project.number) and legacy flat
// (owner / project as integer) formats. An empty or missing owner field is
// treated as an error.
func LoadYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml config %q: %w", path, err)
	}
	return parseYAML(data)
}

// parseYAML decodes raw YAML bytes into a Config.
//
// Detection strategy: inspect the raw YAML document to determine whether the
// "project" key maps to a mapping node (nested format) or a scalar node
// (legacy flat format where project is an integer). This avoids type-mismatch
// errors from yaml.Unmarshal when formats are mixed.
func parseYAML(data []byte) (*Config, error) {
	// Use a generic node tree to inspect the "project" value type.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root := doc.Content[0]
		if isProjectMapping(root) {
			return parseYAMLNested(data)
		}
	}

	// "project" is absent or a scalar — treat as legacy flat format.
	return parseYAMLFlat(data)
}

// isProjectMapping returns true when the YAML mapping node contains a "project"
// key whose value is itself a mapping (i.e. nested format).
func isProjectMapping(root *yaml.Node) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		val := root.Content[i+1]
		if key.Value == "project" {
			return val.Kind == yaml.MappingNode
		}
	}
	return false
}

// parseYAMLNested handles the current nested format (project.owner / project.number).
// It also supports a hybrid format where owner/repo live at the top level
// alongside a nested project block with just the number.
func parseYAMLNested(data []byte) (*Config, error) {
	var nested yamlConfigNested
	if err := yaml.Unmarshal(data, &nested); err != nil {
		return nil, fmt.Errorf("parse yaml (nested): %w", err)
	}

	// Resolve owner: prefer project.owner, fall back to top-level owner, then
	// the legacy github: block (last fallback — #3859).
	owner := nested.Project.Owner
	if owner == "" {
		owner = nested.Owner
	}
	if owner == "" {
		owner = nested.GitHub.Owner
	}
	if owner == "" {
		return nil, fmt.Errorf("config.yaml: owner is required (set project.owner, top-level owner, or github.owner)")
	}

	// Resolve owner_type: prefer project.owner_type, fall back to top-level
	ownerType := nested.Project.OwnerType
	if ownerType == "" {
		ownerType = nested.OwnerType
	}

	// Resolve repo: prefer project.repo, fall back to top-level repo, then the
	// legacy github: block (last fallback — #3859).
	repo := nested.Project.Repo
	if repo == "" {
		repo = nested.Repo
	}
	if repo == "" {
		repo = nested.GitHub.Repo
	}

	cfg := DefaultConfig()
	cfg.Owner = owner
	cfg.OwnerType = normalizeOwnerType(ownerType)
	cfg.ProjectNumber = nested.Project.Number
	cfg.Projects = nested.Projects
	if err := applyProjectEntries(cfg); err != nil {
		return nil, err
	}
	cfg.DefaultRepo = repo
	if nested.LogLevel != "" {
		cfg.LogLevel = nested.LogLevel
	}
	cfg.GitHubUser = nested.GitHubUser
	cfg.GitHubAuth = nested.GitHubAuth
	cfg.Sanitization = nested.Sanitization
	cfg.FeedbackLoop = nested.FeedbackLoop
	cfg.Telemetry = nested.Platform.Telemetry
	cfg.PlatformEnabled = nested.Platform.Enabled
	cfg.PlatformURL = nested.Platform.APIURL
	cfg.LicenseKey = nested.Platform.LicenseKey
	cfg.RemoteCommands = nested.RemoteCommands
	cfg.AgentTeams = nested.AgentTeams
	cfg.Autonomous = nested.Autonomous
	cfg.ReadyToShip = nested.ReadyToShip
	cfg.Knowledge = nested.Knowledge
	cfg.PipelineExecutor = nested.PipelineExecutor
	cfg.Pipeline = nested.Pipeline
	cfg.Routing = nested.Routing
	cfg.ModelRouting = nested.ModelRouting
	cfg.UI = nested.UI
	cfg.SchemaVersion = nested.SchemaVersion
	cfg.Forges = nested.Forges
	cfg.Notifications = nested.Notifications
	cfg.Notifiers = nested.Notifiers
	if len(nested.Project.SizeToEstimate) > 0 {
		cfg.SizeToEstimate = nested.Project.SizeToEstimate
	}
	migrateV1ToV2(cfg)
	return cfg, nil
}

func applyProjectEntries(cfg *Config) error {
	if len(cfg.Projects) == 0 {
		return nil
	}
	seenNames := make(map[string]struct{}, len(cfg.Projects))
	seenNumbers := make(map[int]struct{}, len(cfg.Projects))
	defaultIndex := -1
	for i, project := range cfg.Projects {
		name := strings.TrimSpace(project.Name)
		if name == "" {
			return fmt.Errorf("config.yaml: projects[%d].name is required", i)
		}
		if project.Number <= 0 {
			return fmt.Errorf("config.yaml: projects[%d].number must be positive", i)
		}
		if _, exists := seenNames[name]; exists {
			return fmt.Errorf("config.yaml: duplicate project name %q", name)
		}
		if _, exists := seenNumbers[project.Number]; exists {
			return fmt.Errorf("config.yaml: duplicate project number %d", project.Number)
		}
		seenNames[name] = struct{}{}
		seenNumbers[project.Number] = struct{}{}
		cfg.Projects[i].Name = name
		if project.Default {
			if defaultIndex >= 0 {
				return fmt.Errorf("config.yaml: projects may contain only one default")
			}
			defaultIndex = i
		}
	}
	if defaultIndex < 0 {
		defaultIndex = 0
		cfg.Projects[0].Default = true
	}
	cfg.ProjectNumber = cfg.Projects[defaultIndex].Number
	return nil
}

// parseYAMLFlat handles the legacy flat format (bare owner / project as integer).
func parseYAMLFlat(data []byte) (*Config, error) {
	var flat yamlConfigFlat
	if err := yaml.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("parse yaml (flat): %w", err)
	}
	// Resolve owner: prefer top-level owner, fall back to the legacy github:
	// block (last fallback — #3859).
	owner := flat.Owner
	if owner == "" {
		owner = flat.GitHub.Owner
	}
	if owner == "" {
		return nil, fmt.Errorf("config.yaml: owner is required but missing (set top-level owner or github.owner)")
	}
	// Resolve repo: prefer top-level defaultRepo, fall back to the legacy
	// github: block (last fallback — #3859).
	repo := flat.DefaultRepo
	if repo == "" {
		repo = flat.GitHub.Repo
	}
	cfg := DefaultConfig()
	cfg.Owner = owner
	cfg.DefaultRepo = repo
	if flat.LogLevel != "" {
		cfg.LogLevel = flat.LogLevel
	}
	if flat.Project != 0 {
		cfg.ProjectNumber = flat.Project
	} else {
		cfg.ProjectNumber = flat.ProjectNumber
	}
	cfg.Projects = flat.Projects
	if err := applyProjectEntries(cfg); err != nil {
		return nil, err
	}
	cfg.OwnerType = normalizeOwnerType(flat.OwnerType)
	cfg.GitHubUser = flat.GitHubUser
	cfg.GitHubAuth = flat.GitHubAuth
	cfg.Sanitization = flat.Sanitization
	cfg.FeedbackLoop = flat.FeedbackLoop
	cfg.Telemetry = flat.Telemetry
	cfg.RemoteCommands = flat.RemoteCommands
	cfg.AgentTeams = flat.AgentTeams
	cfg.Autonomous = flat.Autonomous
	cfg.ReadyToShip = flat.ReadyToShip
	cfg.Knowledge = flat.Knowledge
	cfg.PipelineExecutor = flat.PipelineExecutor
	cfg.Pipeline = flat.Pipeline
	cfg.Routing = flat.Routing
	cfg.ModelRouting = flat.ModelRouting
	cfg.UI = flat.UI
	cfg.SchemaVersion = flat.SchemaVersion
	cfg.Forges = flat.Forges
	cfg.Notifications = flat.Notifications
	cfg.Notifiers = flat.Notifiers
	if len(flat.SizeToEstimate) > 0 {
		cfg.SizeToEstimate = flat.SizeToEstimate
	}
	migrateV1ToV2(cfg)
	return cfg, nil
}

// normalizeOwnerType returns "org" or "user". Defaults to "org" for empty/unknown values.
func normalizeOwnerType(raw string) string {
	switch raw {
	case "user":
		return "user"
	default:
		return "org"
	}
}

// migrateV1ToV2 upgrades a v1 config (missing schema_version) to v2 by
// inserting a default forges.github block pointing at github.com. The
// migration is in-memory only — the YAML file on disk is never rewritten.
// Calling it on an already-v2 config is a no-op (idempotent).
func migrateV1ToV2(cfg *Config) {
	if cfg.SchemaVersion != "" {
		return // already v2+
	}
	if cfg.Forges == nil {
		cfg.Forges = make(map[string]*ForgeConfigEntry)
	}
	if _, ok := cfg.Forges["github"]; !ok {
		cfg.Forges["github"] = &ForgeConfigEntry{
			Kind:    "github",
			BaseURL: "https://github.com",
		}
	}
	cfg.SchemaVersion = "2"
}

// ValidateForgeConfig checks forge configuration constraints. Returns a
// multi-error (joined with errors.Join) listing all validation failures.
// repos may be nil when there is no autonomous.repositories block.
func ValidateForgeConfig(forges map[string]*ForgeConfigEntry, repos map[string]*RepositoryConfig) error {
	var errs []error
	validKinds := map[string]bool{"github": true, "gitlab": true}

	for id, entry := range forges {
		if entry == nil {
			continue
		}
		// Unknown forge kind
		if entry.Kind != "" && !validKinds[entry.Kind] {
			errs = append(errs, fmt.Errorf("forges.%s: unknown kind %q (valid: github, gitlab)", id, entry.Kind))
		}
		// Missing base_url for non-github forges
		if entry.Kind == "gitlab" && entry.BaseURL == "" {
			errs = append(errs, fmt.Errorf("forges.%s: base_url is required for kind=gitlab", id))
		}
		// Warn when InsecureSkipTLS is enabled — this disables certificate verification.
		if entry.InsecureSkipTLS && entry.Kind == "gitlab" {
			fmt.Fprintf(os.Stderr, "WARNING: forges.%s: insecure_skip_tls=true disables TLS certificate verification; use ca_bundle instead when possible\n", id)
		}
		// Auth method validation
		if entry.AuthMethod != "" {
			switch entry.AuthMethod {
			case "token", "app", "pat", "oauth2", "ci_job_token", "deploy_token":
				// valid
			default:
				errs = append(errs, fmt.Errorf("forges.%s: unknown auth_method %q (valid: token, app, pat, oauth2, ci_job_token, deploy_token)", id, entry.AuthMethod))
			}
			if entry.AuthMethod == "token" && entry.TokenEnv == "" {
				errs = append(errs, fmt.Errorf("forges.%s: auth_method=token requires token_env to be set", id))
			}
		}
	}

	// Dangling forge references in per-repo config
	for repoName, repoCfg := range repos {
		if repoCfg == nil || repoCfg.Forge == "" {
			continue
		}
		if _, ok := forges[repoCfg.Forge]; !ok {
			errs = append(errs, fmt.Errorf("autonomous.repositories.%s: forge %q references unknown forge key (declared forges: %v)", repoName, repoCfg.Forge, forgeKeys(forges)))
		}
	}

	return errors.Join(errs...)
}

// forgeKeys returns sorted keys of a ForgeConfigEntry map for deterministic error messages.
func forgeKeys(m map[string]*ForgeConfigEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SuppressGHWarning returns true when the user has opted out of gh CLI
// deprecation warnings via github_auth.suppress_gh_warning: true in config.yaml.
func (c *Config) SuppressGHWarning() bool {
	if c == nil || c.GitHubAuth == nil {
		return false
	}
	return c.GitHubAuth.SuppressGHWarning
}

// ResolveGitHubUser returns the GitHub username to use for API calls against
// the workspace owner. It delegates to ResolveGitHubUserForOwner(c.Owner) — use
// ResolveGitHubUserForOwner directly when the target repo's owner differs from
// the workspace owner (cross-org dispatch, e.g. Acme-Community → acmebot).
func (c *Config) ResolveGitHubUser() string {
	return c.ResolveGitHubUserForOwner(c.Owner)
}

// ResolveGitHubUserForOwner returns the GitHub username configured for the
// PASSED owner (not necessarily the workspace owner).
// Priority: explicit github_user > github_auth.users[owner] > "".
//
// The explicit github_user always wins — it is the per-repo identity declared
// in that repo's config. github_auth.users is the multi-org fallback map, keyed
// by owner, so a cross-org dispatch resolves the identity for the TARGET repo's
// owner rather than only the workspace root owner. This is what lets the
// scheduler act as acmebot for an Acme-Community target even when
// the workspace root owner is something else.
func (c *Config) ResolveGitHubUserForOwner(owner string) string {
	if c == nil {
		return ""
	}
	if c.GitHubUser != "" {
		return c.GitHubUser
	}
	if c.GitHubAuth != nil && owner != "" {
		if user, ok := c.GitHubAuth.Users[owner]; ok {
			return user
		}
	}
	return ""
}

// ResolveToken returns a GitHub PAT from config, respecting the resolution
// priority: per-project token > per-org token[owner] > "".
// The env:VAR_NAME syntax is resolved by reading the named environment variable.
// Returns ("", nil) when no config-based token is found (caller should fall
// back to GITHUB_TOKEN env var or gh auth token).
func (c *Config) ResolveToken(owner string) (string, error) {
	if c.GitHubAuth == nil {
		return "", nil
	}

	// Per-project token takes priority.
	if c.GitHubAuth.Token != "" {
		return resolveEnvRef(c.GitHubAuth.Token)
	}

	// Per-org token mapping.
	if len(c.GitHubAuth.Tokens) > 0 && owner != "" {
		if ref, ok := c.GitHubAuth.Tokens[owner]; ok && ref != "" {
			return resolveEnvRef(ref)
		}
	}

	return "", nil
}

// resolveEnvRef resolves a token value that may use env:VAR_NAME syntax.
// If the value starts with "env:", the named environment variable is looked up.
// Non-env: values are returned as-is (direct PAT strings).
// Returns an error if the referenced env var is missing or empty.
func resolveEnvRef(ref string) (string, error) {
	const prefix = "env:"
	if !strings.HasPrefix(ref, prefix) {
		// Direct token value — return as-is.
		return ref, nil
	}
	varName := ref[len(prefix):]
	if varName == "" {
		return "", fmt.Errorf("invalid env: reference %q: variable name is empty", ref)
	}
	val := os.Getenv(varName)
	if val == "" {
		return "", fmt.Errorf("environment variable %q referenced by config token is not set or empty", varName)
	}
	return val, nil
}
