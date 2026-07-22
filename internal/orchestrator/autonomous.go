// Package orchestrator — autonomous.go implements the AutonomousScheduler,
// a continuous monitoring loop that sits above the existing Scheduler. It
// builds the cross-repo dependency graph (via depgraph), determines the
// optimal next items to execute based on priority/critical-path analysis,
// fills pipeline slots, and cascades unblocks across repos when items complete.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/focus"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/state"
)

// AutonomousConfig holds configuration for the autonomous scheduler loop.
type AutonomousConfig struct {
	ScanInterval  time.Duration // default 30s — how often to re-scan boards (active cadence)
	MaxConcurrent int           // pipeline slots across all repos
	BudgetCeiling int64         // global token budget (0 = unlimited)
	DebounceRepos bool          // only re-query repos with recent completions
	DryRun        bool          // show what would run without executing
	PickupBacklog bool          // dispatch Backlog items after all Ready items are done (default: false)
	SafetyRails   *SafetyConfig // safety rail overrides (nil = use defaults)

	// IdleScanInterval is the polling cadence used after the loop has been
	// idle for IdleCyclesBeforeBackoff cycles in a row (no candidates AND no
	// running pipelines). When 0 or <= ScanInterval, idle backoff is
	// disabled and the loop ticks at ScanInterval forever. (#3023 phase 1)
	IdleScanInterval time.Duration
	// IdleCyclesBeforeBackoff is the number of consecutive idle cycles
	// before the loop widens to IdleScanInterval. Default 4.
	IdleCyclesBeforeBackoff int

	// Refinement fields
	RefinementEnabled       bool          // default: true — enable refinement scan loop
	RefinementInterval      time.Duration // default: 60s (min 30s) — interval between refinement scans
	RefinementMaxConcurrent int           // default: 1 (max 3) — concurrent refinement slots
	RefinementCooldown      time.Duration // default: 5m — per-issue cooldown after refinement

	// AutoActionable controls whether auto-refined issues go to Ready (true) or Backlog (false).
	AutoActionable bool

	// PerRepoMax is the default per-repository concurrency cap
	// (concurrency.per_repo_max). 0 → 1 (serialize per repo). Applies to any
	// repo without a RepositoryMaxConcurrent override.
	PerRepoMax int

	// RepositoryMaxConcurrent maps repo names (short or fully-qualified) to a
	// per-repo concurrency cap that overrides PerRepoMax. The scheduler skips
	// dispatching from a repo when its currently-running count meets or
	// exceeds this cap. Populated by main.go from
	// concurrency.repository_overrides.
	RepositoryMaxConcurrent map[string]int

	// GraphCacheTTL is how long a built dependency graph is reused before
	// BuildGraph is called again. Set to 0 to disable caching (always rebuild).
	// Default: 5 minutes. Cache is always invalidated on TriggerRescan.
	GraphCacheTTL time.Duration

	// DisableEpicBlockedByCascade, when true, disables the default behaviour
	// where a sub-issue is treated as blocked when its parent epic has an open
	// blockedBy dependency. Set via autonomous.disable_epic_blockedby_cascade
	// in .nightgauge/config.yaml. Default: false (cascade enabled).
	DisableEpicBlockedByCascade bool

	// StuckEpicDetectionEnabled gates the no-silent-stall watchdog (#4073). When
	// true (default), each idle cycle scans for epics that are open with open
	// sub-issues but have zero eligible work, no running pipeline, and no
	// sub-issue actively recovering — surfacing them instead of looking "done".
	StuckEpicDetectionEnabled bool
	// StuckEpicWebhookURL is the resolved Discord webhook URL for stalled-epic
	// alerts (resolved by main.go from the configured env var). Empty disables
	// the Discord sink; detection still surfaces via state + the CLI.
	StuckEpicWebhookURL string
	// StuckEpicReAlertAfter is the cooldown before re-alerting on the same
	// still-stalled epic. Default: 6h.
	StuckEpicReAlertAfter time.Duration

	// ExcludeLabels lists human-only labels (autonomous.exclude_labels,
	// default ["owner-action"]) that the candidate loop refuses to dispatch —
	// see the skip check beside the type:epic exclusion below. Resolved by
	// main.go via config.AutonomousConfig.ResolvedExcludeLabels(); empty here
	// falls back to defaultExcludeLabels so callers that construct this
	// struct directly (tests, other entry points) still get the safety net.
	// Issue #317.
	ExcludeLabels []string
}

// defaultExcludeLabels mirrors config.DefaultExcludeLabels without importing
// the config package here (autonomous.go stays config-shape-agnostic; main.go
// is the only place that reads config.yaml and bridges it into this runtime
// struct). Both must be changed together.
var defaultExcludeLabels = []string{"owner-action"}

// resolvedExcludeLabels returns cfg.ExcludeLabels, falling back to
// defaultExcludeLabels when unset (#317).
func resolvedExcludeLabels(cfg []string) []string {
	if len(cfg) == 0 {
		return defaultExcludeLabels
	}
	return cfg
}

// excludedLabelMatch reports whether any of `labels` case-insensitively
// matches an entry in `excludeLabels`, returning the matched label for
// diagnostics. Shared by the autonomous candidate loop and the epic-enqueue
// sub-issue filter (scheduler.go EnqueueEpic) so both paths agree on what
// counts as a human-only issue (#317).
func excludedLabelMatch(labels, excludeLabels []string) (string, bool) {
	for _, exclude := range excludeLabels {
		for _, label := range labels {
			if strings.EqualFold(label, exclude) {
				return label, true
			}
		}
	}
	return "", false
}

// ExcludedLabelMatch is the exported form of excludedLabelMatch for callers
// outside the orchestrator package that need the identical human-only-label
// check before their own enqueue path (e.g. the `queue add` CLI command's
// single-issue branch — see cmd/nightgauge/main.go). Issue #317.
func ExcludedLabelMatch(labels, excludeLabels []string) (string, bool) {
	return excludedLabelMatch(labels, excludeLabels)
}

// DefaultAutonomousConfig returns sensible defaults for the autonomous scheduler.
func DefaultAutonomousConfig() AutonomousConfig {
	return AutonomousConfig{
		ScanInterval:            30 * time.Second,
		MaxConcurrent:           3,
		BudgetCeiling:           0,
		DebounceRepos:           true,
		DryRun:                  false,
		RefinementEnabled:       true,
		RefinementInterval:      60 * time.Second,
		RefinementMaxConcurrent: 1,
		RefinementCooldown:      5 * time.Minute,
		// #3023 phase 1: when the loop sits idle for 4 cycles in a row
		// (default 2 minutes at base 30s cadence) it widens to 5 minutes.
		// Snaps back to base on any rescan trigger or candidate appearance.
		IdleScanInterval:          5 * time.Minute,
		IdleCyclesBeforeBackoff:   4,
		GraphCacheTTL:             5 * time.Minute,
		StuckEpicDetectionEnabled: true,
		StuckEpicReAlertAfter:     6 * time.Hour,
		ExcludeLabels:             defaultExcludeLabels,
	}
}

// RunningItem tracks an in-flight pipeline execution.
type RunningItem struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	StartedAt string `json:"startedAt"`
}

// CompletedItem records a successfully completed pipeline run.
type CompletedItem struct {
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	CompletedAt string `json:"completedAt"`
}

// FailedItem records a pipeline run that ended in failure.
//
// Items are deduplicated by `{repo, number}` — if the same issue fails
// multiple times, one FailedItem is kept and `AttemptCount` is incremented,
// `FailedAt`/`Reason` are updated to the latest attempt, and `FirstFailedAt`
// is preserved from the first attempt. State files written before this
// schema existed may contain duplicate entries per issue; `loadState`
// migrates them on the next read.
type FailedItem struct {
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	FailedAt      string `json:"failedAt"`
	Reason        string `json:"reason,omitempty"`
	AttemptCount  int    `json:"attemptCount,omitempty"`
	FirstFailedAt string `json:"firstFailedAt,omitempty"`
}

// RefinementItem tracks an in-flight or completed refinement operation.
type RefinementItem struct {
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	FailedAt    string `json:"failedAt,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// AutonomousState is the persistent state of the autonomous scheduler.
type AutonomousState struct {
	Status        string          `json:"status"` // "running", "paused", "stopped", "complete", "budget_exhausted", "safety_tripped"
	StartedAt     string          `json:"startedAt"`
	LastScanAt    string          `json:"lastScanAt"`
	Running       []RunningItem   `json:"running"`
	Completed     []CompletedItem `json:"completed"`
	Failed        []FailedItem    `json:"failed"`
	Remaining     int             `json:"remaining"`
	TokensSpent   int64           `json:"tokensSpent"`
	TokensCeiling int64           `json:"tokensCeiling"`
	CyclesRun     int             `json:"cyclesRun"`
	Safety        *SafetyState    `json:"safety,omitempty"`

	// LifetimeIssueFailures tracks failures across sessions (NOT cleared on
	// Resume). Used to enforce the per-issue terminal-failure cap so a single
	// broken issue cannot be retried indefinitely. Cleared only by an explicit
	// ClearIssueFailures call (manual triage). Key: "repo#number".
	// See #3020 — original incident burned $64.77 retrying the same issue 3x
	// at $21.59/run because Resume() reset the per-session counter.
	LifetimeIssueFailures map[string]int `json:"lifetimeIssueFailures,omitempty"`

	// Refinement state
	RefinementRunning    []RefinementItem `json:"refinementRunning,omitempty"`
	RefinementCompleted  []RefinementItem `json:"refinementCompleted,omitempty"`
	RefinementFailed     []RefinementItem `json:"refinementFailed,omitempty"`
	LastRefinementScanAt string           `json:"lastRefinementScanAt,omitempty"`

	// Diagnostics from the most recent prioritize() pass. Populated every
	// scan cycle so the UI / IPC consumers can surface "why is autonomous
	// idle?" without grepping go-backend.log. Cleared/overwritten each
	// cycle — historical per-cycle data is in the rotating logs.
	LastNodeCount        int            `json:"lastNodeCount,omitempty"`
	LastCandidateCount   int            `json:"lastCandidateCount,omitempty"`
	LastRejectionReasons map[string]int `json:"lastRejectionReasons,omitempty"`

	// Why the scheduler is currently paused/safety-tripped. Recorded on
	// every Pause()/safety-trip transition so future investigations don't
	// depend on log archeology (Issue #3251). Cleared on Resume()/Run().
	// PauseReason is a short human-readable explanation (e.g. "user
	// requested via UI", "haltQueueOnSlotFailure: issue #3239 failed at
	// pr-merge", "safety: rate limit exceeded"). PauseTriggeredBy is a
	// structured tag identifying the caller ("user", "haltQueueOnSlotFailure",
	// "safety:rate-limit", "safety:circuit-breaker", "safety:health-gate",
	// "safety:epic-checkpoint"). PausedAt is ISO-8601.
	PauseReason      string `json:"pauseReason,omitempty"`
	PauseTriggeredBy string `json:"pauseTriggeredBy,omitempty"`
	PausedAt         string `json:"pausedAt,omitempty"`

	// QuotaCooldownUntil is the ISO-8601 wall-clock time until which the
	// scheduler must NOT dispatch any new pipeline runs because the upstream
	// Anthropic 5-hour rate-limit bucket is known-exhausted. Set when any
	// pipeline run terminates with TerminalKindRateLimitQuotaExhausted (or
	// TerminalKindStreamIdleTimeout when a `resetsAt=<unix>` hint is present
	// in the failure text). Auto-clears at expiry — the next runCycle tick
	// resumes dispatching without operator action.
	//
	// This is distinct from `PauseReason`/`Status="paused"` (operator
	// action) and `Status="safety_tripped"` (manual triage required). A
	// quota cooldown is a transient wait. Issue #3431 — closes the gap left
	// by the per-issue 1-hour backoff in #3398/#3386 which let other Ready
	// items continue dispatching into the same exhausted bucket and burning
	// $2-14 of front-loaded cache_creation tokens per dead session.
	QuotaCooldownUntil  string `json:"quotaCooldownUntil,omitempty"`
	QuotaCooldownReason string `json:"quotaCooldownReason,omitempty"`

	// ConfigWarnings holds config-coherence warnings produced at Run() startup.
	// Cleared and re-generated on each Run() call. Persisted so
	// `autonomous status` can surface them after the fact.
	// Issue #3640.
	ConfigWarnings []ConfigWarning `json:"configWarnings,omitempty"`

	// StuckEpics holds the epics flagged as stalled on the most recent idle scan
	// (#4073). Re-computed each idle cycle; surfaced via `autonomous stuck-epics`
	// and the IPC snapshot. Empty when nothing is stalled.
	StuckEpics []StuckEpic `json:"stuckEpics,omitempty"`
}

// AutonomousStatusChange is the payload fired by onStatusChange whenever
// state.Status transitions. Carries everything the VSCode extension needs to
// refresh the autonomous badge without re-querying via IPC (Issue #3251).
type AutonomousStatusChange struct {
	Status           string `json:"status"`
	PauseReason      string `json:"pauseReason,omitempty"`
	PauseTriggeredBy string `json:"pauseTriggeredBy,omitempty"`
	RunningCount     int    `json:"runningCount"`
	Remaining        int    `json:"remaining"`
}

// MaxLifetimeFailuresPerIssue is the cap on cross-session failures for a single
// issue before autonomous refuses to retry it without explicit user action.
// Default 2 — first failure may be transient/flaky, second confirms a real
// problem. After the cap is hit, the dispatch loop trips safety mode with a
// reason naming the offending issue so the user can triage and ClearIssueFailures.
const MaxLifetimeFailuresPerIssue = 2

// streamIdleTimeoutBackoff is the per-issue backoff applied when a pipeline
// stage fails with TerminalKindStreamIdleTimeout (Issue #3398). The cause is
// environmental — typically the Anthropic 5-hour rate-limit bucket being
// depleted with overage rejected (`out_of_credits`). One hour is long enough
// to clear most rate-limit windows without keeping the issue parked
// indefinitely, and is far longer than the default exponential backoff
// (which would re-fire under the same API conditions and burn another full
// run's tokens).
const streamIdleTimeoutBackoff = 1 * time.Hour

// stallKillBackoff is the per-issue backoff applied when a pipeline stage is
// stall-killed (agent exceeded the idle or hard-cap threshold). Stall-kills
// are transient — the agent ran out of time or context, not because the issue
// has a code defect. 30 minutes gives the system breathing room before the
// next attempt without parking the issue for the full stream-idle-timeout
// window. The issue does NOT count toward the lifetime failure cap so that
// repeated infrastructure stalls cannot permanently block a valid issue.
const stallKillBackoff = 30 * time.Minute

// apiOverloadedBackoff is the per-issue backoff applied when the Anthropic API
// returns a 529 "Overloaded" response (TerminalKindApiOverloaded). Overload is
// a brief capacity blip that clears within minutes — unlike a depleted
// rate-limit bucket — so the backoff is short and NO global quota cooldown is
// applied: only the affected issue waits while the rest of the queue keeps
// flowing. Issue #3835 (WS4).
const apiOverloadedBackoff = 5 * time.Minute

// blockedDependencyBackoff is the modest per-issue backoff applied when a
// dispatched issue is deferred because its blockedBy dependencies are still
// open (TerminalKindBlockedDependency). A deferral is NOT a failure, so the
// issue stays eligible (board → Ready) — the backoff only prevents a hot loop
// if the scheduler re-dispatches it while the blocker is still open. The
// blocker-close requeue (refreshBlockerStates / promoteUnblockedToReady)
// re-dispatches it promptly once the blocker actually closes, so this floor
// need not be long. Issue #305.
const blockedDependencyBackoff = 5 * time.Minute

// githubNetworkOutageCooldown is the GLOBAL dispatch cooldown applied when the
// pipeline-start preflight finds api.github.com unreachable
// (TerminalKindGitHubNetworkOutage). Unlike a quota dip there is no published
// reset time — most blips clear within seconds — so two minutes balances fast
// auto-recovery against hot-looping new dispatches into a dead network. The
// cooldown is global (not per-issue) because an outage affects every repo
// equally: any other Ready item dispatched during the window would fail the
// same preflight. Issue #4002.
const githubNetworkOutageCooldown = 2 * time.Minute

// quotaResetGrace is the buffer added to a parsed `resetsAt` Unix timestamp
// before the global cooldown expires. Anthropic resets the bucket exactly at
// `resetsAt`, but a small clock-skew margin avoids re-arming a millisecond
// before the API agrees that quota is back. Issue #3431.
const quotaResetGrace = 60 * time.Second

// resetsAtPattern extracts `resetsAt=<unix>` from a kill-marker / failure-text
// substring. Matches the canonical form emitted by skillRunner's
// `[rate-limit-quota-exhausted]` marker (e.g.
// `... five_hour bucket; resetsAt=1778428800`). Issue #3431.
var resetsAtPattern = regexp.MustCompile(`resetsAt=(\d{9,11})`)

// parseQuotaResetsAt looks for a `resetsAt=<unix>` token in failure text and
// returns the corresponding wall-clock time. Returns ok=false when the token
// is absent, malformed, or in the past (a past reset is useless for
// scheduling — fall back to the streamIdleTimeoutBackoff floor instead).
func parseQuotaResetsAt(text string, now time.Time) (time.Time, bool) {
	if text == "" {
		return time.Time{}, false
	}
	m := resetsAtPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return time.Time{}, false
	}
	unix, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	t := time.Unix(unix, 0)
	if !t.After(now) {
		return time.Time{}, false
	}
	return t, true
}

// computeQuotaCooldownUntil returns the wall-clock time until which the global
// dispatch cooldown should hold given a failure text and current time. When
// the text carries a `resetsAt=<unix>` hint, the cooldown runs until that
// time + a small grace; otherwise it falls back to the streamIdleTimeoutBackoff
// floor. The returned time is always at least `now + streamIdleTimeoutBackoff`
// — a parsed reset that's sooner than the floor is widened to the floor (the
// last failure proved the bucket is exhausted; trusting an aggressively-near
// reset risks immediately re-dispatching into the same wall).
func computeQuotaCooldownUntil(failureText string, now time.Time) time.Time {
	floor := now.Add(streamIdleTimeoutBackoff)
	if t, ok := parseQuotaResetsAt(failureText, now); ok {
		hinted := t.Add(quotaResetGrace)
		if hinted.After(floor) {
			return hinted
		}
	}
	return floor
}

// applyQuotaCooldownLocked sets the global Anthropic-quota cooldown that
// suspends all dispatches until the upstream rate-limit bucket resets.
// Caller must hold as.mu.
//
// The cooldown is the LATER of the existing cooldown (if any) and the new
// computed value — multiple concurrent failures all reporting the same
// resetsAt should not shorten the cooldown.
//
// Issue #3431.
func (as *AutonomousScheduler) applyQuotaCooldownLocked(label, key, failureText string) {
	now := time.Now()
	until := computeQuotaCooldownUntil(failureText, now)

	// Honor the longer of any existing cooldown and the new value.
	if existing, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil); err == nil {
		if existing.After(until) {
			until = existing
		}
	}

	as.state.QuotaCooldownUntil = until.UTC().Format(time.RFC3339)
	as.state.QuotaCooldownReason = fmt.Sprintf(
		"%s (Anthropic API quota exhausted; first observed via %s) — dispatch suspended until %s",
		label, key, until.UTC().Format(time.RFC3339))
	log.Printf("autonomous: GLOBAL quota cooldown until %s (triggered by %s for %s)",
		until.UTC().Format(time.RFC3339), label, key)
}

// applyGitHubQuotaCooldownLocked suspends ALL dispatch until the GitHub
// REST/GraphQL rate-limit bucket resets at resetAt. It is the GitHub-API
// sibling of applyQuotaCooldownLocked (which handles the Anthropic bucket) and
// deliberately writes the SAME QuotaCooldownUntil suspend field so the single,
// source-agnostic quotaCooldownActiveLocked gate in runCycle enforces both.
// Unlike the Anthropic path it takes an explicit reset time (the GitHub bucket
// publishes a precise X-RateLimit-Reset; there is no fuzzy text hint to parse)
// and never extends past one hour. Honors the longer of any existing cooldown
// and the new value so an active Anthropic cooldown is never shortened.
// Caller must hold as.mu. Issue #3896.
func (as *AutonomousScheduler) applyGitHubQuotaCooldownLocked(resetAt time.Time, detail string) {
	now := time.Now()
	until := resetAt
	// Defend against a missing/past reset: back off at least a minute so we
	// don't hot-loop dispatching into the same exhausted bucket.
	if !until.After(now) {
		until = now.Add(time.Minute)
	}
	// Cap at one hour — the GitHub bucket is hourly; anything longer is a bad
	// reading and would needlessly stall the queue.
	if max := now.Add(time.Hour); until.After(max) {
		until = max
	}
	if existing, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil); err == nil {
		if existing.After(until) {
			until = existing
		}
	}
	as.state.QuotaCooldownUntil = until.UTC().Format(time.RFC3339)
	as.state.QuotaCooldownReason = fmt.Sprintf(
		"GitHub API quota low (%s) — dispatch suspended until %s",
		detail, until.UTC().Format(time.RFC3339))
	log.Printf("autonomous: GitHub quota cooldown until %s (%s)",
		until.UTC().Format(time.RFC3339), detail)
}

// quotaCooldownActiveLocked returns true and the deadline when the global
// quota cooldown is still in effect. Auto-clears stale state in-place.
// Caller must hold as.mu.
func (as *AutonomousScheduler) quotaCooldownActiveLocked() (bool, time.Time) {
	if as.state.QuotaCooldownUntil == "" {
		return false, time.Time{}
	}
	deadline, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil)
	if err != nil {
		// Malformed — clear it so we don't re-fail every cycle.
		as.state.QuotaCooldownUntil = ""
		as.state.QuotaCooldownReason = ""
		return false, time.Time{}
	}
	if !time.Now().Before(deadline) {
		// Expired — clear it. Next runCycle resumes dispatching.
		as.state.QuotaCooldownUntil = ""
		as.state.QuotaCooldownReason = ""
		log.Printf("autonomous: quota cooldown expired at %s, dispatching resumes",
			deadline.UTC().Format(time.RFC3339))
		return false, time.Time{}
	}
	return true, deadline
}

// autonomousStateFile is the relative path under workspace root for state persistence.
const autonomousStateFile = ".nightgauge/autonomous/state.json"

// graphIncompleteThreshold is the fraction of dropped board items above which
// the scheduler logs a WARNING that its scheduling decisions may be incorrect.
const graphIncompleteThreshold = 0.10

// AutonomousScheduler coordinates cross-repo pipeline execution.
// It sits above the existing Scheduler — uses the depgraph to determine
// what to run and the existing pipeline infrastructure to run it.
type AutonomousScheduler struct {
	scheduler *Scheduler
	ghClient  *gh.Client
	// allRepos is the full, pristine repo list from construction. Used as the
	// source for FilterRepos so repeated filter calls can widen/narrow the
	// active set idempotently (rather than monotonically shrinking).
	allRepos []depgraph.RepoConfig
	repos    []depgraph.RepoConfig

	// enabledRepos is the resolved allowlist passed to FilterRepos (fully-
	// qualified "owner/repo" strings). Set as a side-effect of FilterRepos so
	// that Run() can pass it to ValidateAutonomousConfig without requiring a
	// dependency on *config.Config in the orchestrator package. Nil when no
	// allowlist was applied (scan-all mode). Issue #3640.
	enabledRepos []string
	repoAliases  map[string]string
	config       AutonomousConfig
	state        *AutonomousState
	safetyRails  *SafetyRails

	// cascadeTracker fires when N pipeline failures land inside the
	// configured sliding window (#3605 bullet C). When tripped, Pause is
	// invoked with triggeredBy=safety:cascading-failures and is NOT in
	// any auto-resume self-clear path — clearing requires explicit user
	// Resume(). The tracker is shared across repos in the workspace so a
	// fault that fans out across repos (e.g. GitHub rate-limit storm)
	// is detected as a single cascade rather than three independent
	// per-repo failures.
	cascadeTracker *CascadeTracker

	workspaceRoot string

	// attention is the single authoritative writer for the Action Center
	// DecisionRequest store (ADR 015). Producers (work exhaustion, owner-action
	// handoff, cascade pause, blockedBy deferral, watchdog) raise through this
	// store; nil-safe via raiseAttention. Constructed at NewAutonomousScheduler
	// rooted at workspaceRoot.
	attention *attention.Store

	// survivalWindowDays is the post-merge survival observation window (#4151),
	// resolved from pipeline.survival.window_days at construction. 0 → the sweep
	// applies survival.DefaultWindowDays.
	survivalWindowDays int

	// inReviewRecoveryAttempts bounds how many times reconcileStuckInReviewPRs
	// re-runs pr-merge for a stuck-in-review issue (keyed "repo#number") before
	// leaving it for human triage. Guarded by mu.
	inReviewRecoveryAttempts map[string]int

	// blockedReadyPRIssues is the set (keyed "repo#number") of dispatchable
	// issues whose OPEN PR is BLOCKED — a failing required check or branch-
	// protection block that no pipeline retry can clear (only a human can). It
	// is refreshed on FRESH graph builds by refreshBlockedReadyPRs (one
	// gh-pr-list per repo, quota-safe) and read by prioritize() to skip
	// re-dispatching such issues. Without this guard a failed pr-merge reverts
	// the issue to Ready and the ENTIRE pipeline re-runs against a PR that still
	// can't merge — the wasteful churn seen across bowlsheet #234/#244/#254/#245.
	// Non-destructive: it never moves board status, so once the PR unblocks,
	// merges, or closes, the issue is eligible again on the next fresh scan.
	// Guarded by mu.
	blockedReadyPRIssues map[string]bool

	mu      sync.Mutex
	running bool
	stopCh  chan struct{} // signals dispatch loop only — one reader

	// stopRefinementCh signals the refinement goroutine to exit. Kept separate
	// from stopCh so each channel has exactly one reader, eliminating the race
	// where the refinement goroutine drains the shared buffer before the main
	// dispatch loop can observe it. (#3029)
	stopRefinementCh chan struct{}

	// stopRequested is set to true as soon as Stop() is called, before the
	// main loop has had a chance to drain stopCh. The in-flight runCycle
	// reads this flag before every enqueueItem so that a Stop pressed mid-
	// cycle cannot leak additional autonomous.dispatch events to the
	// TypeScript side. Cleared by Run() at startup.
	stopRequested bool

	// rescanCh receives a signal when a pipeline completes, triggering
	// an immediate re-scan instead of waiting for the next tick.
	rescanCh chan struct{}

	// onCycleComplete is an optional callback fired after each scan cycle
	// completes. Used for testing to synchronize on cycle completion.
	onCycleComplete func()

	// dispatcher is the execution substrate for pipeline items. When set via
	// SetDispatcher, all dispatch calls go through this interface instead of
	// the legacy onDispatch/fallback callbacks. Takes precedence over onDispatch.
	dispatcher Dispatcher

	// onDispatch is called when the autonomous scheduler wants to dispatch an
	// issue to the pipeline. When set, the scheduler delegates to this callback
	// (e.g. IPC emit to TypeScript extension) instead of using the Go queue.
	onDispatch func(owner, repo string, issueNumber int, title string)

	// onStatusChange is fired whenever state.Status transitions. Used by the
	// IPC server to push an `autonomous.statusChanged` event to the VSCode
	// extension so the status bar badge stays in sync without polling.
	// (Issue #3251.)
	onStatusChange func(snapshot AutonomousStatusChange)

	// perIssueFailureCount tracks how many times each issue has failed since the
	// last Resume(). Used to compute per-issue exponential backoff.
	// Key: "repo#number" (e.g. "acme/mobile#152")
	perIssueFailureCount map[string]int

	// retryBackoff holds the earliest time each failed issue may be retried.
	// Key: "repo#number". Cleared on Resume() so explicit user action bypasses
	// the backoff. Not persisted — resets to zero on server restart.
	retryBackoff map[string]time.Time

	// conflictRestartCount tracks how many times each issue hit the legacy
	// fresh-branch conflict-restart path. As of #4072 the primary conflict path
	// is the BRANCH-PRESERVING conflict-recovery loop: pr-merge captures the
	// conflict context and rewinds (within the same pipeline run) to feature-dev
	// to resolve it on the SAME branch, bounded by
	// pipeline.recovery.conflict_recovery.max_dev_redispatch — that loop never
	// deletes the branch and never round-trips through this counter. This
	// counter remains only as the circuit-breaker exemption for any residual
	// fresh-branch restart signal (conflict-restart-{N}.json); the modern skill
	// no longer emits that signal, so it is effectively dormant. These restarts
	// do NOT count toward the circuit breaker — they are infrastructure
	// failures, not code-quality failures. After MaxConflictRestarts attempts
	// the issue falls through to regular failure handling.
	// Key: "repo#number". Cleared on Resume().
	conflictRestartCount map[string]int

	// recentClosures records the time each issue was added to state.Completed
	// during the current session. Used as a guard in reconcileStateAgainstGraph
	// to prevent re-admitting issues that were just closed — GitHub's
	// read-after-write is not instantaneous and may return stale OPEN state
	// within the first ~60 seconds after a closure (Issue #3661).
	// Key: "repo#number". Not persisted — intentionally resets on restart
	// (the guard window is short and the race window vanishes after a cold start).
	recentClosures map[string]time.Time

	// refinementSem limits concurrent refinements (buffered channel as semaphore).
	refinementSem chan struct{}

	// refinementCooldown maps "repo#number" → earliest-next-refine time.
	// Prevents re-processing if pipeline:refined is manually removed quickly.
	refinementCooldown map[string]time.Time

	// refinementFailures tracks consecutive refinement failures per issue.
	// Key: "repo#number". NOT shared with dispatch circuit breaker.
	refinementFailures map[string]int

	// onRefinementDispatch is an optional callback for IPC mode.
	// When nil, refinement is spawned directly via execution.Manager.
	onRefinementDispatch func(owner, repo string, issueNumber int)

	// refinementUnavailableOnce ensures the "refinement not wired" log is
	// emitted at most once per scheduler lifetime, instead of once per cycle.
	refinementUnavailableOnce sync.Once

	// graphCache is the most recently built dependency graph. Protected by mu.
	// Nil when the cache is cold (first run or after invalidation).
	graphCache   *depgraph.Graph
	graphCacheAt time.Time

	// alertedStuckEpics records when each stalled epic ("repo#number") was last
	// alerted, so the watchdog re-alerts at most once per StuckEpicReAlertAfter
	// cooldown instead of every idle cycle (#4073). Guarded by mu.
	alertedStuckEpics map[string]time.Time

	// stuckEpicHistoryFn overrides the per-issue history reader used by the
	// stuck-epic watchdog (active-recovery check + blocker reason). Nil in
	// production (reads the workspace history JSONL); set in tests.
	stuckEpicHistoryFn func(repo string, number int) (*state.V2RunRecord, bool)

	// buildGraphFn is the function used to build the dependency graph.
	// Defaults to wrapping depgraph.BuildGraph; overridable in tests without
	// a real GitHub client.
	buildGraphFn func(ctx context.Context) (*depgraph.Graph, error)

	// resolveDepStatesFn batch-resolves the true GitHub state ("OPEN"/"CLOSED")
	// of dependency keys ("owner/repo#number") that have NO node in the graph
	// — the graph only builds nodes from project-board items (depgraph/
	// builder.go), so a blockedBy/depends-on edge can point at an issue
	// (commonly an epic) that was never added to any board (#306). Defaults to
	// wrapping resolveIssueStatesByKey against a real IssueService;
	// overridable in tests without a real GitHub client. A nil func, or a nil
	// return, means resolution is unavailable — callers must fail closed,
	// never assume the dep is satisfied.
	resolveDepStatesFn func(ctx context.Context, keys []string) map[string]string

	// refinementEmptyCache tracks when each repo last returned zero refinement
	// candidates. Repos that return empty results are skipped for
	// refinementEmptyCacheTTL to avoid burning GitHub API quota every 60s on
	// repos that have nothing to refine. Protected by mu.
	// Key: "owner/repo". Cleared when candidates are found for that repo.
	refinementEmptyCache map[string]time.Time
}

// MaxConflictRestarts bounds the LEGACY fresh-branch conflict-restart path
// (residual conflict-restart-{N}.json signal) before treating the issue as a
// true failure. The modern conflict path (#4072) is the branch-preserving
// conflict-recovery loop, whose own bound is
// pipeline.recovery.conflict_recovery.max_dev_redispatch (default 2) — it
// resolves the conflict in-place via a feature-dev rewind and never reaches
// this counter. This constant only matters if a residual fresh-branch signal is
// somehow present, which the current pr-merge skill no longer emits.
const MaxConflictRestarts = 3

// NewAutonomousScheduler creates a new autonomous scheduler that wraps the
// existing Scheduler and uses the depgraph for cross-repo coordination.
func NewAutonomousScheduler(
	scheduler *Scheduler,
	ghClient *gh.Client,
	repos []depgraph.RepoConfig,
	repoAliases map[string]string,
	cfg AutonomousConfig,
	workspaceRoot string,
) *AutonomousScheduler {
	// Build safety config from the autonomous config.
	safetyCfg := DefaultSafetyConfig()
	if cfg.BudgetCeiling > 0 {
		safetyCfg.BudgetCeiling = cfg.BudgetCeiling
	}
	if cfg.SafetyRails != nil {
		if cfg.SafetyRails.BudgetCeiling > 0 {
			safetyCfg.BudgetCeiling = cfg.SafetyRails.BudgetCeiling
		}
		if cfg.SafetyRails.CircuitBreakerMax > 0 {
			safetyCfg.CircuitBreakerMax = cfg.SafetyRails.CircuitBreakerMax
		}
		if cfg.SafetyRails.RateLimitPerHour > 0 {
			safetyCfg.RateLimitPerHour = cfg.SafetyRails.RateLimitPerHour
		}
		if cfg.SafetyRails.HealthGateMin > 0 {
			safetyCfg.HealthGateMin = cfg.SafetyRails.HealthGateMin
		}
		safetyCfg.EpicCheckpoint = cfg.SafetyRails.EpicCheckpoint
	}

	// Clamp refinement concurrency to [1, 3].
	refinementSlots := cfg.RefinementMaxConcurrent
	if refinementSlots <= 0 {
		refinementSlots = 1
	}
	if refinementSlots > 3 {
		refinementSlots = 3
	}

	// Clone repos into allRepos so FilterRepos can restart from the pristine
	// list. Without this, repeated FilterRepos calls monotonically shrink the
	// active set and user-toggled repos cannot be re-added without a restart.
	pristine := make([]depgraph.RepoConfig, len(repos))
	copy(pristine, repos)

	as := &AutonomousScheduler{
		scheduler:   scheduler,
		ghClient:    ghClient,
		allRepos:    pristine,
		repos:       repos,
		repoAliases: repoAliases,
		config:      cfg,
		safetyRails: NewSafetyRails(safetyCfg),
		// #3605 bullet C: cascading-failure circuit breaker. Defaults are
		// 3 failures inside a 30-minute sliding window; env-var overrides
		// (NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD / _WINDOW) take effect
		// without a rebuild. See docs/CASCADE_CIRCUIT_BREAKER.md.
		cascadeTracker: NewCascadeTracker(CascadeTrackerConfig{}),
		workspaceRoot:  workspaceRoot,
		state: &AutonomousState{
			Status:        "stopped",
			TokensCeiling: safetyCfg.BudgetCeiling,
		},
		// stopCh and stopRefinementCh are both buffered (cap=1) so Stop()'s
		// non-blocking sends always succeed even when either loop is mid-cycle.
		// Each channel has exactly one reader (dispatch loop vs. refinement loop)
		// so neither goroutine can drain the other's signal. (#3029)
		stopCh:               make(chan struct{}, 1),
		stopRefinementCh:     make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1), // buffered so sends never block
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
		refinementSem:        make(chan struct{}, refinementSlots),
		refinementCooldown:   make(map[string]time.Time),
		refinementFailures:   make(map[string]int),
		refinementEmptyCache: make(map[string]time.Time),
	}

	// Wire the default graph builder. Tests override this field to inject a fake
	// without requiring a real GitHub client.
	as.buildGraphFn = func(ctx context.Context) (*depgraph.Graph, error) {
		return depgraph.BuildGraph(ctx, as.ghClient, as.repos, as.repoAliases)
	}

	// Wire the default off-board dependency resolver. Tests override this
	// field to inject a fake without requiring a real GitHub client (#306).
	as.resolveDepStatesFn = func(ctx context.Context, keys []string) map[string]string {
		return resolveIssueStatesByKey(ctx, gh.NewIssueService(as.ghClient), keys)
	}

	// Wire the Action Center DecisionRequest store (ADR 015). The store is the
	// single authoritative writer for `.nightgauge/attention/`; producers raise
	// through it and resolutions execute registry verbs. The steer writer pins
	// operator steer text as next-stage context; the trace listener audits every
	// terminal transition of a run-scoped request into the ADR-013 decision
	// trace.
	if workspaceRoot != "" {
		as.attention = attention.New(workspaceRoot)
		as.attention.SetSteerWriter(func(req *attention.DecisionRequest, steerText string) error {
			return WriteOperatorSteer(workspaceRoot, req.Context.Issue, steerText, req.Context.Stage)
		})
		as.attention.Subscribe(as.auditAttentionTransition)
		// Share the one store with the inner Scheduler so run-scoped producers
		// (budget ceiling, branch-protection, auth failure) raise through the
		// same single writer.
		scheduler.SetAttention(as.attention)
	}

	// Load any persisted state from a previous run.
	as.loadState()

	return as
}

// Run starts the autonomous scheduler loop. It blocks until the context is
// cancelled, Stop() is called, the budget is exhausted, or all items complete.
func (as *AutonomousScheduler) Run(ctx context.Context) error {
	// Catch panics so an unexpected crash is logged to autonomous-exits.jsonl
	// rather than silently killing the goroutine and leaving the UI stuck in
	// RUNNING state forever.
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			if len(stack) > 4096 {
				stack = stack[:4096]
			}
			panicMsg := fmt.Sprintf("%v", r)
			log.Printf("autonomous: PANIC: %s\n%s", panicMsg, stack)
			as.mu.Lock()
			as.state.Status = "crashed"
			as.running = false
			as.mu.Unlock()
			as.writeCrashExitEvent(panicMsg, stack)
		}
	}()

	as.mu.Lock()
	if as.running {
		as.mu.Unlock()
		return fmt.Errorf("autonomous scheduler is already running")
	}
	as.running = true
	as.stopRequested = false
	as.state.Status = "running"
	as.state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	// Clear stale pause provenance — a fresh Run is not a paused state.
	as.state.PauseReason = ""
	as.state.PauseTriggeredBy = ""
	as.state.PausedAt = ""
	as.fireStatusChangeLocked()
	as.mu.Unlock()
	as.persistState()

	// Run config-coherence validation at startup and persist any warnings.
	// Non-fatal — a panic inside ValidateAutonomousConfig is recovered there.
	// Issue #3640.
	func() {
		warnings := ValidateAutonomousConfig(as.config, as.enabledRepos, nil, nil)
		as.mu.Lock()
		as.state.ConfigWarnings = warnings
		as.mu.Unlock()
		as.persistState()
		for _, w := range warnings {
			log.Printf("autonomous: CONFIG-WARN [%s]: %s", w.Kind, w.Message)
		}
	}()

	// Wire up the pipeline-complete callback so we can cascade unblocks.
	prevCallback := as.scheduler.onPipelineComplete
	as.scheduler.OnPipelineComplete(func(repo string, issue int, runtime *state.RuntimeState, success bool) {
		// CLI mode (non-IPC) has no conflict-restart signal — pass false.
		// Derive the terminal failure kind from the failed stage's error
		// text so as.onPipelineComplete can route specific environmental
		// kinds (e.g. stream_idle_timeout) through dedicated retry policies
		// instead of penalizing the issue for an upstream-API problem.
		// (#3398)
		terminalKind := ""
		failureDetail := ""
		if !success && runtime != nil {
			snap := runtime.Snapshot()
			if snap.Stage != "" {
				if errMsg, ok := snap.StageErrors[string(snap.Stage)]; ok {
					terminalKind = ClassifyTerminalKind(errMsg)
					failureDetail = errMsg
				}
			}
		}
		as.onPipelineComplete(repo, issue, success, false, terminalKind, failureDetail)
		// Chain to any previously registered callback.
		if prevCallback != nil {
			prevCallback(repo, issue, runtime, success)
		}
	})

	// #3023 phase 1: adaptive cadence. We use a *Timer* (single-shot,
	// resettable) instead of a Ticker so we can vary the interval between
	// fires based on observed activity. Base = ScanInterval; after
	// IdleCyclesBeforeBackoff consecutive idle cycles, the loop widens to
	// IdleScanInterval. Any candidate appearance, dispatch, or rescan
	// trigger snaps cadence back to base immediately.
	cadence := as.scanCadence()
	timer := time.NewTimer(cadence)
	defer timer.Stop()
	consecutiveIdleCycles := 0

	// Start refinement scan loop (parallel to dispatch loop)
	if as.config.RefinementEnabled {
		go func() {
			refinementTicker := time.NewTicker(as.config.RefinementInterval)
			defer refinementTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-as.stopRefinementCh:
					return
				case <-refinementTicker.C:
					as.runRefinementCycle(ctx)
				}
			}
		}()
	}

	// Recover orphaned running items from a previous crashed session.
	// These are issues that were dispatched but whose onPipelineComplete callback
	// never fired because the session died. They are stuck "In progress" on the
	// board and blocking dispatch slots.
	as.recoverOrphanedRunning(ctx)

	// Initial scan
	as.runCycle(ctx)
	consecutiveIdleCycles = as.updateIdleCounterAfterCycle(consecutiveIdleCycles)

	resetTimer := func(d time.Duration) {
		if !timer.Stop() {
			// Drain channel non-blockingly — Stop returns false either
			// because the timer already fired (channel has a value) or
			// has been stopped previously (channel empty).
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
	}

	for {
		select {
		case <-ctx.Done():
			as.complete("cancelled")
			return ctx.Err()
		case <-as.stopCh:
			as.complete("stopped")
			return nil
		case <-as.rescanCh:
			// Immediate re-scan triggered by pipeline completion or by an
			// external caller via TriggerRescan() (#3023 phase 1).
			consecutiveIdleCycles = 0 // explicit signal — reset cadence
			as.runCycle(ctx)
			consecutiveIdleCycles = as.updateIdleCounterAfterCycle(consecutiveIdleCycles)
			resetTimer(as.scanCadence())
		case <-timer.C:
			as.runCycle(ctx)
			consecutiveIdleCycles = as.updateIdleCounterAfterCycle(consecutiveIdleCycles)
			next := as.scanCadence()
			if consecutiveIdleCycles >= as.idleCyclesBeforeBackoff() && as.config.IdleScanInterval > next {
				next = as.config.IdleScanInterval
			}
			resetTimer(next)
		}
	}
}

// scanCadence returns the current base scan interval, clamped to a sane
// minimum (5s) so a misconfigured config can't wedge the CPU.
func (as *AutonomousScheduler) scanCadence() time.Duration {
	d := as.config.ScanInterval
	if d < 5*time.Second {
		d = 30 * time.Second
	}
	return d
}

// idleCyclesBeforeBackoff returns the configured idle threshold with a sane
// fallback for zero values.
func (as *AutonomousScheduler) idleCyclesBeforeBackoff() int {
	n := as.config.IdleCyclesBeforeBackoff
	if n <= 0 {
		return 4
	}
	return n
}

// updateIdleCounterAfterCycle returns the new consecutiveIdleCycles value.
// Increments when the system has nothing useful to dispatch:
//   - Truly idle: no running pipelines and no queued candidates.
//   - No effective capacity: all slots consumed once per-repo caps are applied
//     (e.g. MaxConcurrent=3 but the only active repo has cap=1 and it's full).
//
// Either condition backs off to IdleScanInterval after IdleCyclesBeforeBackoff
// consecutive cycles. When a slot opens, TriggerRescan fires an immediate
// cycle so the longer interval never delays pickup.
func (as *AutonomousScheduler) updateIdleCounterAfterCycle(prev int) int {
	as.mu.Lock()
	remaining := as.state.Remaining
	runningCount := len(as.state.Running)
	as.mu.Unlock()
	if remaining == 0 && runningCount == 0 {
		return prev + 1
	}
	if as.effectiveAvailableSlots() == 0 {
		return prev + 1
	}
	return 0
}

// TriggerRescan signals the scheduler loop to run a cycle immediately,
// bypassing the polling timer. Safe to call from any goroutine. Idempotent:
// if a rescan is already pending, this is a no-op. The send is
// non-blocking because rescanCh is buffered (cap=1).
//
// Used by:
//   - Pipeline completion cascade (existing).
//   - IPC method autonomous.rescan (#3023 phase 1) — fired by the VSCode
//     extension after local promote / queue / drag-to-Ready actions so
//     dispatch is instant rather than waiting for the next poll.
//   - Future: SSE event subscriber receiving project board change events
//     from the platform webhook ingester (#3024 phase 2).
func (as *AutonomousScheduler) TriggerRescan() {
	as.mu.Lock()
	as.graphCache = nil // invalidate: next cycle fetches a fresh graph
	as.mu.Unlock()
	select {
	case as.rescanCh <- struct{}{}:
	default:
		// Channel full — a rescan is already pending, which will pick up
		// any state change the caller wanted to surface anyway.
	}
}

// InvalidateGraphCache clears the cached dependency graph so the next
// runCycle fetches a fresh one from GitHub. Use after manual board changes
// (e.g., project sync-status) that bypass the normal pipeline event path.
func (as *AutonomousScheduler) InvalidateGraphCache() {
	as.mu.Lock()
	as.graphCache = nil
	as.mu.Unlock()
}

// SetDispatcher replaces the execution substrate used for all pipeline
// dispatches. When set, Dispatch calls go through the provided Dispatcher
// interface instead of the legacy onDispatch/fallback callbacks.
// Call before Run() to take effect.
func (as *AutonomousScheduler) SetDispatcher(d Dispatcher) {
	as.dispatcher = d
}

// OnDispatch sets a callback for dispatching issues to the pipeline.
// When set, the autonomous scheduler delegates to this callback instead of
// using the Go scheduler queue directly — allowing the TypeScript extension
// to run the pipeline through HeadlessOrchestrator.
// Deprecated: prefer SetDispatcher for new code.
func (as *AutonomousScheduler) OnDispatch(fn func(owner, repo string, issueNumber int, title string)) {
	as.onDispatch = fn
}

// NotifyComplete notifies the autonomous scheduler that a dispatched pipeline
// run has completed. Called by the IPC server when the TypeScript extension
// finishes a run that was dispatched via autonomous.dispatch.
//
// conflictRestart should be true when the failure was caused by unresolvable
// merge conflicts (concurrent-branch collision). In that case the circuit
// breaker is NOT incremented — conflicts are infrastructure failures, not
// code-quality failures. After MaxConflictRestarts attempts the issue is
// treated as a true failure.
//
// terminalFailureKind, when non-empty, names the terminal failure category
// (see failure_handler.go). The scheduler routes specific environmental
// kinds (e.g. stream_idle_timeout, network_unavailable) through dedicated
// retry policies that don't penalize the issue for an upstream-API problem.
// Pass "" when unknown.
//
// failureDetail is the raw failure text observed by the caller (e.g. the
// kill marker from skillRunner). Used to extract `resetsAt=<unix>` for
// quota-exhausted failures so the global Anthropic-quota cooldown can run
// until the actual bucket reset rather than a fixed 1-hour floor (#3431).
// Optional — empty falls back to the floor.
//
// Defense-in-depth (#3439): when the caller passes terminalFailureKind=""
// but failureDetail carries a recognizable kill marker, re-classify on the
// Go side via ClassifyTerminalKind. The CLI/auto path already does this
// (Run() wraps the scheduler's onPipelineComplete with a classifier).
// Without this fallback, a TS-side regex miss silently routes a quota-
// exhausted failure into the GENERIC branch, bypassing the global cooldown
// (#3434) and re-enabling the multi-issue cascade #3434 was meant to fix.
func (as *AutonomousScheduler) NotifyComplete(repo string, issueNumber int, success bool, conflictRestart bool, terminalFailureKind string, failureDetail string) {
	if !success && terminalFailureKind == "" && failureDetail != "" {
		if reclassified := ClassifyTerminalKind(failureDetail); reclassified != "" {
			log.Printf("autonomous: NotifyComplete reclassified empty terminalFailureKind from failureDetail: %s (issue %s#%d)",
				reclassified, repo, issueNumber)
			terminalFailureKind = reclassified
		}
	}
	as.onPipelineComplete(repo, issueNumber, success, conflictRestart, terminalFailureKind, failureDetail)
}

// FilterRepos restricts the scheduler to only scan repos in the given set.
// Called before Run() (and on every VS Code autonomous.start/resume) to
// scope scanning to the workspace, or to the user's explicit allowlist via
// `autonomous.enabled_repos` in config.yaml.
//
// Format: ["nightgauge/nightgauge", "acme/platform", ...].
// An empty slice is a no-op (keeps existing repos). Filtering is applied
// against the pristine full repo list (allRepos) so repeated calls can
// widen the active set as well as narrow it — previously filtered-out
// repos can be re-added without rebuilding the scheduler.
//
// In addition to filtering the repo scan list, this prunes persisted state
// (completed, failed, running) and in-memory backoff maps so that entries
// from repos outside the allowlist are not displayed or retried.
func (as *AutonomousScheduler) FilterRepos(workspaceRepos []string) {
	if len(workspaceRepos) == 0 {
		return
	}

	// Record the allowlist so Run() can pass it to ValidateAutonomousConfig.
	// Store a copy to guard against callers mutating the slice after return.
	// Issue #3640.
	cp := make([]string, len(workspaceRepos))
	copy(cp, workspaceRepos)
	as.enabledRepos = cp

	allowed := make(map[string]bool, len(workspaceRepos))
	for _, r := range workspaceRepos {
		allowed[strings.ToLower(r)] = true
	}

	// Filter against allRepos (pristine) rather than as.repos so callers can
	// widen the set after a previous narrowing. Fall back to as.repos if
	// allRepos was never populated (defensive — older construction paths).
	source := as.allRepos
	if len(source) == 0 {
		source = as.repos
	}

	var filtered []depgraph.RepoConfig
	for _, r := range source {
		if allowed[strings.ToLower(r.FullName())] {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		log.Printf("autonomous: FilterRepos matched 0 of %d repos — keeping all to avoid empty scan", len(source))
		return
	}
	log.Printf("autonomous: filtered repos to %d of %d (allowlist applied)", len(filtered), len(source))
	as.repos = filtered

	// Prune persisted state and in-memory maps so entries from repos outside
	// the allowlist don't appear in the UI or affect backoff/circuit-breaker.
	as.pruneStateToRepos(allowed)
}

// pruneStateToRepos removes completed, failed, and running entries whose repo
// is not in the allowed set. Also cleans per-issue backoff and failure maps.
func (as *AutonomousScheduler) pruneStateToRepos(allowed map[string]bool) {
	as.mu.Lock()
	defer as.mu.Unlock()

	prevCompleted := len(as.state.Completed)
	prevFailed := len(as.state.Failed)

	as.state.Completed = filterCompleted(as.state.Completed, allowed)
	as.state.Failed = filterFailed(as.state.Failed, allowed)
	as.state.Running = filterRunning(as.state.Running, allowed)

	// Prune refinement state
	as.state.RefinementRunning = filterRefinement(as.state.RefinementRunning, allowed)
	as.state.RefinementCompleted = filterRefinement(as.state.RefinementCompleted, allowed)
	as.state.RefinementFailed = filterRefinement(as.state.RefinementFailed, allowed)

	// Clean in-memory maps (keyed as "owner/repo#number").
	for key := range as.perIssueFailureCount {
		if !repoKeyAllowed(key, allowed) {
			delete(as.perIssueFailureCount, key)
		}
	}
	for key := range as.retryBackoff {
		if !repoKeyAllowed(key, allowed) {
			delete(as.retryBackoff, key)
		}
	}
	for key := range as.conflictRestartCount {
		if !repoKeyAllowed(key, allowed) {
			delete(as.conflictRestartCount, key)
		}
	}
	for key := range as.refinementCooldown {
		if !repoKeyAllowed(key, allowed) {
			delete(as.refinementCooldown, key)
		}
	}
	for key := range as.refinementFailures {
		if !repoKeyAllowed(key, allowed) {
			delete(as.refinementFailures, key)
		}
	}

	pruned := (prevCompleted - len(as.state.Completed)) + (prevFailed - len(as.state.Failed))
	if pruned > 0 {
		log.Printf("autonomous: pruned %d out-of-workspace entries from persisted state", pruned)
		as.persistStateLocked()
	}
}

// repoKeyAllowed checks whether a map key like "nightgauge/repo#123" belongs
// to one of the allowed repos.
func repoKeyAllowed(key string, allowed map[string]bool) bool {
	// Keys are formatted as "owner/repo#number" by the graph.
	idx := strings.LastIndex(key, "#")
	if idx <= 0 {
		return true // malformed key — keep it to be safe
	}
	return allowed[strings.ToLower(key[:idx])]
}

func filterCompleted(items []CompletedItem, allowed map[string]bool) []CompletedItem {
	out := make([]CompletedItem, 0, len(items))
	for _, item := range items {
		if allowed[strings.ToLower(item.Repo)] {
			out = append(out, item)
		}
	}
	return out
}

func filterFailed(items []FailedItem, allowed map[string]bool) []FailedItem {
	out := make([]FailedItem, 0, len(items))
	for _, item := range items {
		if allowed[strings.ToLower(item.Repo)] {
			out = append(out, item)
		}
	}
	return out
}

func filterRunning(items []RunningItem, allowed map[string]bool) []RunningItem {
	out := make([]RunningItem, 0, len(items))
	for _, item := range items {
		if allowed[strings.ToLower(item.Repo)] {
			out = append(out, item)
		}
	}
	return out
}

func filterRefinement(items []RefinementItem, allowed map[string]bool) []RefinementItem {
	out := make([]RefinementItem, 0, len(items))
	for _, item := range items {
		if allowed[strings.ToLower(item.Repo)] {
			out = append(out, item)
		}
	}
	return out
}

// IsRunning reports whether the scheduler goroutine is currently active.
// Used by the IPC server to distinguish "goroutine alive but blocked" from
// "fresh start with stale persisted status".
func (as *AutonomousScheduler) IsRunning() bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.running
}

// RunningSiblings returns `owner/repo#number` keys for every in-flight
// pipeline EXCEPT the (repo, issueNumber) caller is asking about. Used by
// the stage-exit diagnostic writer (#3605) so each daily record carries the
// set of sibling pipelines that were live when this stage exited — the
// cross-pipeline forensic anchor for processTree-reaper kills and other
// cross-talk patterns. Returns an empty slice when no siblings ran (the
// common case) so callers can short-circuit the omitempty branch.
func (as *AutonomousScheduler) RunningSiblings(repo string, issueNumber int) []string {
	as.mu.Lock()
	defer as.mu.Unlock()
	out := make([]string, 0, len(as.state.Running))
	for _, r := range as.state.Running {
		if r.Repo == repo && r.Number == issueNumber {
			continue
		}
		out = append(out, fmt.Sprintf("%s#%d", r.Repo, r.Number))
	}
	return out
}

// RateLimitRemaining returns the GitHub GraphQL bucket reading from the
// shared rate-limit tracker. Returns -1 when the tracker is unavailable or
// has no entry yet. Used by the stage-exit diagnostic writer (#3605) to
// snapshot the bucket state at the moment a stage exited so retros can
// correlate near-empty buckets with the failure they likely caused.
func (as *AutonomousScheduler) RateLimitRemaining() int {
	if as.ghClient == nil {
		return -1
	}
	tracker := as.ghClient.RateLimitTracker()
	if tracker == nil {
		return -1
	}
	entry, _, err := tracker.Get(as.ghClient.RateLimitTrackerUser())
	if err != nil || entry == nil {
		return -1
	}
	return entry.Remaining
}

// gitHubQuotaSnapshot reads the GitHub rate-limit bucket from the shared
// tracker (zero network cost — the tracker is refreshed from X-RateLimit-*
// response headers on every gh call). Returns ok=false when the tracker is
// unavailable or has no observation yet, in which case callers must treat the
// quota as UNKNOWN and never block on it. Issue #3896.
func (as *AutonomousScheduler) gitHubQuotaSnapshot() (remaining, limit int, resetAt time.Time, ok bool) {
	if as.ghClient == nil {
		return 0, 0, time.Time{}, false
	}
	tracker := as.ghClient.RateLimitTracker()
	if tracker == nil {
		return 0, 0, time.Time{}, false
	}
	entry, _, err := tracker.Get(as.ghClient.RateLimitTrackerUser())
	if err != nil || entry == nil {
		return 0, 0, time.Time{}, false
	}
	return entry.Remaining, entry.Limit, time.Unix(entry.ResetAt, 0), true
}

// minGitHubQuotaHeadroom is the GitHub REST/GraphQL rate-limit floor below
// which the scheduler stops dispatching and waits for the bucket to reset.
// Sized for one full pipeline run (board reads + status updates + PR ops +
// post-merge verify) and kept equal to the TS pipeline-start preflight
// threshold (HeadlessOrchestrator.MIN_RATE_LIMIT_HEADROOM) so the Go gate
// defers an issue BEFORE the TS preflight would terminally fail it. Issue #3896.
const minGitHubQuotaHeadroom = 200

// boardRecoveryTimeout bounds the background board-status recovery ops
// (revert-to-Ready, move-to-Done, promote-unblocked). It must exceed the github
// client's rate-limit reset wait (≈75m, gh.maxFullExhaustionWait) so that when
// the GitHub bucket is exhausted these ops PAUSE and complete after reset
// instead of dying at a 30–60s deadline ("context deadline exceeded") and
// leaving the issue stuck "In progress" — the #94 / recurring failure. These
// run in background goroutines doing idempotent board moves, so a long ceiling
// is safe. Issue #3976.
const boardRecoveryTimeout = 80 * time.Minute

// Stop signals the autonomous scheduler to stop.
//
// In addition to signalling stopCh (which the main select loop drains between
// cycles), this sets stopRequested so that an already-running runCycle can
// observe the stop request mid-iteration and skip remaining dispatches. Without
// that flag, runCycle would keep calling onDispatch for every candidate in its
// already-built list — emitting autonomous.dispatch events to the TypeScript
// extension after the user pressed "Stop Autonomous".
func (as *AutonomousScheduler) Stop() {
	as.mu.Lock()
	defer as.mu.Unlock()
	if !as.running {
		return
	}
	as.stopRequested = true
	select {
	case as.stopCh <- struct{}{}:
	default:
	}
	select {
	case as.stopRefinementCh <- struct{}{}:
	default:
	}
}

// SetMaxConcurrent updates the global concurrent-dispatch ceiling at runtime.
// Returns (previous, new) so callers can log/no-op when unchanged. Values <= 0
// are ignored (treated as "no change") to prevent accidentally zeroing the
// scheduler. The new value takes effect on the next runCycle — already-running
// pipelines are not affected.
//
// Wired via IPC `autonomous.setMaxConcurrent` so the SettingsPanel can change
// the slot count without forcing a full restart of autonomous mode (#dup-slots).
func (as *AutonomousScheduler) SetMaxConcurrent(n int) (int, int) {
	as.mu.Lock()
	defer as.mu.Unlock()
	prev := as.config.MaxConcurrent
	if n <= 0 || n == prev {
		return prev, prev
	}
	as.config.MaxConcurrent = n
	// Wake the loop so the new ceiling is applied immediately rather than
	// waiting for the next ScanInterval tick.
	select {
	case as.rescanCh <- struct{}{}:
	default:
	}
	return prev, n
}

// OnStatusChange registers a callback fired whenever state.Status transitions.
// Used by the IPC server to push an `autonomous.statusChanged` event to the
// VSCode extension so the badge stays in sync (Issue #3251).
func (as *AutonomousScheduler) OnStatusChange(fn func(AutonomousStatusChange)) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.onStatusChange = fn
}

// fireStatusChangeLocked invokes the onStatusChange callback (if set) outside
// the mutex so listener code can call back into the scheduler without
// deadlocking. Caller must hold as.mu.
func (as *AutonomousScheduler) fireStatusChangeLocked() {
	if as.onStatusChange == nil {
		return
	}
	snap := AutonomousStatusChange{
		Status:           as.state.Status,
		PauseReason:      as.state.PauseReason,
		PauseTriggeredBy: as.state.PauseTriggeredBy,
		RunningCount:     len(as.state.Running),
		Remaining:        as.state.Remaining,
	}
	cb := as.onStatusChange
	go cb(snap)
}

// Pause transitions the scheduler to paused state. It keeps state but stops
// scheduling new items. The loop continues ticking but runCycle becomes a no-op.
//
// reason is a short human-readable explanation; triggeredBy is a structured
// tag identifying the caller (Issue #3251). Both are persisted to state.json
// so investigations don't depend on log archeology.
//
// #3444 — Defense-in-depth: when triggeredBy == "haltQueueOnSlotFailure" and a
// quota cooldown is currently active, decline the pause. The TS-side
// haltQueueOnSlotFailure handler is supposed to skip the pause when the
// failure was environmental, but a regex miss there would still allow the
// pause to land — and pause survives until manual Resume, whereas the
// cooldown auto-clears. Skipping here keeps autonomous in "running" so the
// next runCycle tick re-dispatches automatically after the cooldown lapses.
// Real failures (no cooldown set) still pause as expected.
func (as *AutonomousScheduler) Pause(reason, triggeredBy string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.state.Status == "running" {
		// #3444: skip haltQueueOnSlotFailure pause when a fresh quota
		// cooldown is in effect — the cooldown is sufficient to prevent
		// further dispatch and is the auto-recovery path.
		if triggeredBy == "haltQueueOnSlotFailure" && as.state.QuotaCooldownUntil != "" {
			if until, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil); err == nil && time.Now().UTC().Before(until) {
				log.Printf("autonomous: declining haltQueueOnSlotFailure pause — quota cooldown active until %s (%s)",
					as.state.QuotaCooldownUntil, reason)
				return
			}
		}
		as.state.Status = "paused"
		as.state.PauseReason = reason
		as.state.PauseTriggeredBy = triggeredBy
		as.state.PausedAt = time.Now().UTC().Format(time.RFC3339)
		as.persistStateLocked()
		as.fireStatusChangeLocked()
	}
}

// Resume transitions from paused or safety_tripped back to running state.
// When resuming from safety_tripped, the circuit breaker and rate limiter are
// reset so the scheduler can dispatch new items immediately.
// Per-issue backoff and failure counts are also cleared so that all issues
// are eligible for immediate retry on user-initiated resume.
func (as *AutonomousScheduler) Resume() {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.state.Status == "paused" || as.state.Status == "safety_tripped" {
		as.state.Status = "running"
		// Clear pause provenance so a future Pause records a fresh reason.
		as.state.PauseReason = ""
		as.state.PauseTriggeredBy = ""
		as.state.PausedAt = ""
		// Reset safety rails so the circuit breaker doesn't immediately re-trip
		// when the user explicitly chooses to resume after a safety trip.
		if as.safetyRails != nil {
			as.safetyRails.Reset()
		}
		// #3605 bullet C: clearing the cascading-failure breaker is gated on
		// explicit user Resume — never on an automatic / self-clear path. We
		// land here only from operator action, so reset is appropriate.
		if as.cascadeTracker != nil {
			as.cascadeTracker.Reset()
		}
		// Clear per-issue backoff and conflict restart counts so the user's
		// explicit resume bypasses all cooldowns.
		as.perIssueFailureCount = make(map[string]int)
		as.retryBackoff = make(map[string]time.Time)
		as.conflictRestartCount = make(map[string]int)
		as.refinementCooldown = make(map[string]time.Time)
		as.refinementFailures = make(map[string]int)
		as.persistStateLocked()
		as.fireStatusChangeLocked()
		// Trigger an immediate re-scan
		select {
		case as.rescanCh <- struct{}{}:
		default:
		}
	}
}

// ClearQuotaCooldown unconditionally removes the global Anthropic-quota
// cooldown so the next runCycle dispatches without waiting for the recorded
// deadline. Returns (cleared, previousUntil) — cleared=false when no cooldown
// was active (no-op clear), previousUntil carries the deadline that was in
// effect immediately before the clear (empty when none).
//
// Manual escape hatch for the user when the cooldown is stale (Anthropic
// quota proven recovered, false-positive resetsAt parse, etc.) or when the
// user explicitly wants to start dispatching despite the recorded backoff.
// Persists immediately so the cleared state survives a backend restart.
// Issue #3446.
func (as *AutonomousScheduler) ClearQuotaCooldown() (bool, string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	previous := as.state.QuotaCooldownUntil
	if previous == "" {
		return false, ""
	}
	as.state.QuotaCooldownUntil = ""
	as.state.QuotaCooldownReason = ""
	as.persistStateLocked()
	return true, previous
}

// QuotaCooldownSnapshot returns a read-only view of the global dispatch
// cooldown that suspends new pipeline runs while an upstream quota is
// known-exhausted. The same QuotaCooldownUntil field backs both the Anthropic
// 5-hour bucket cooldown (applyQuotaCooldownLocked) and the GitHub API quota
// cooldown (applyGitHubQuotaCooldownLocked), so a single source-agnostic read
// tells a caller whether dispatch is currently suspended for quota reasons.
//
// Unlike quotaCooldownActiveLocked, this accessor is side-effect free — it does
// NOT clear stale/expired state — so it is safe to call from a read-only IPC
// handler. The returned active flag reports whether the recorded deadline is
// still in the future at call time (a malformed deadline reads as inactive).
//
// Issue #3909 — the workflow quota bridge consumes this so a large fan-out can
// distinguish a genuine quota cooldown from a transient status=allowed stall.
func (as *AutonomousScheduler) QuotaCooldownSnapshot() (until, reason string, active bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	until = as.state.QuotaCooldownUntil
	reason = as.state.QuotaCooldownReason
	if until == "" {
		return "", reason, false
	}
	deadline, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return until, reason, false
	}
	return until, reason, time.Now().Before(deadline)
}

// ClearIssueFailures resets the lifetime failure counter for a single issue
// (or all issues if key == ""). Used by the IPC handler that the VSCode UI
// invokes when the user manually triages a chronically-failing issue. Returns
// the number of issues whose counters were cleared.
//
// #3020 — without this, the per-issue terminal-failure cap would permanently
// lock out a fixed issue with no escape hatch short of editing state.json.
func (as *AutonomousScheduler) ClearIssueFailures(key string) int {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.state.LifetimeIssueFailures == nil {
		return 0
	}
	if key == "" {
		n := len(as.state.LifetimeIssueFailures)
		as.state.LifetimeIssueFailures = make(map[string]int)
		as.persistStateLocked()
		return n
	}
	if _, ok := as.state.LifetimeIssueFailures[key]; !ok {
		return 0
	}
	delete(as.state.LifetimeIssueFailures, key)
	// Also clear any session-level backoff so the cleared issue can dispatch
	// immediately on the next scan.
	if as.perIssueFailureCount != nil {
		delete(as.perIssueFailureCount, key)
	}
	if as.retryBackoff != nil {
		delete(as.retryBackoff, key)
	}
	as.persistStateLocked()
	return 1
}

// logIncompleteGraph logs a WARNING when the graph's dropped item count exceeds
// graphIncompleteThreshold, indicating that scheduling decisions may be based on
// an incomplete view of the board.
func logIncompleteGraph(graph *depgraph.Graph) {
	if graph.Stats.DroppedItemsCount <= 0 {
		return
	}
	totalFetched := len(graph.Nodes) + graph.Stats.DroppedItemsCount
	if totalFetched == 0 {
		return
	}
	dropPct := float64(graph.Stats.DroppedItemsCount) / float64(totalFetched)
	if dropPct >= graphIncompleteThreshold {
		log.Printf("autonomous: WARNING graph is incomplete — %d items dropped (%.1f%% of %d fetched); scheduling decisions may be incorrect",
			graph.Stats.DroppedItemsCount, dropPct*100, totalFetched)
	}
}

// issueBackoffDuration returns the backoff delay for the Nth failure of an
// issue. The schedule doubles each failure, capped at 30 minutes:
//
//	1 failure → 2 min, 2 → 4 min, 3 → 8 min, 4 → 16 min, 5+ → 30 min
func issueBackoffDuration(failureCount int) time.Duration {
	if failureCount <= 0 {
		return 0
	}
	minutes := 1 << failureCount // 2, 4, 8, 16, 32 ...
	if minutes > 30 {
		minutes = 30
	}
	return time.Duration(minutes) * time.Minute
}

// Status returns a snapshot of the current autonomous scheduler state.
func (as *AutonomousScheduler) Status() AutonomousState {
	as.mu.Lock()
	defer as.mu.Unlock()
	// Return a deep copy
	snapshot := *as.state
	snapshot.Running = make([]RunningItem, len(as.state.Running))
	copy(snapshot.Running, as.state.Running)
	snapshot.Completed = make([]CompletedItem, len(as.state.Completed))
	copy(snapshot.Completed, as.state.Completed)
	snapshot.Failed = make([]FailedItem, len(as.state.Failed))
	copy(snapshot.Failed, as.state.Failed)
	// Deep copy refinement state
	snapshot.RefinementRunning = make([]RefinementItem, len(as.state.RefinementRunning))
	copy(snapshot.RefinementRunning, as.state.RefinementRunning)
	snapshot.RefinementCompleted = make([]RefinementItem, len(as.state.RefinementCompleted))
	copy(snapshot.RefinementCompleted, as.state.RefinementCompleted)
	snapshot.RefinementFailed = make([]RefinementItem, len(as.state.RefinementFailed))
	copy(snapshot.RefinementFailed, as.state.RefinementFailed)
	// Include latest safety state
	if as.safetyRails != nil {
		safetySnap := as.safetyRails.State()
		snapshot.Safety = &safetySnap
	}
	return snapshot
}

// runCycle performs one scan-prioritize-dispatch cycle.
func (as *AutonomousScheduler) runCycle(ctx context.Context) {
	as.mu.Lock()
	if as.state.Status != "running" {
		as.mu.Unlock()
		return // paused or terminal state
	}
	// #3431: global Anthropic-quota cooldown gate. When the upstream
	// 5-hour rate-limit bucket is known-exhausted, suspend ALL dispatches
	// until the bucket resets. This is the missing global-half of #3386 /
	// #3425 — the per-issue 1-hour backoff in #3398 protected only the
	// failing issue while letting other Ready items continue feeding work
	// into the same exhausted quota and burning $2-14 of front-loaded
	// cache_creation tokens per dead session.
	if active, deadline := as.quotaCooldownActiveLocked(); active {
		as.state.LastScanAt = time.Now().UTC().Format(time.RFC3339)
		reason := as.state.QuotaCooldownReason
		// #3446: Surface the cooldown as a rejection reason so TS-side
		// consumers (status bar, output channel) can render "Autonomous:
		// cooldown until …" without polling additional state. Recorded on
		// every cooldown-blocked cycle so the user sees activity rather
		// than a silent idle.
		as.state.LastRejectionReasons = map[string]int{
			"quota-cooldown": 1,
		}
		as.state.LastCandidateCount = 0
		as.mu.Unlock()
		log.Printf("autonomous: skipping dispatch — quota cooldown active until %s (%.0fs remaining): %s",
			deadline.UTC().Format(time.RFC3339),
			time.Until(deadline).Seconds(), reason)
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	// #3896: GitHub API quota headroom gate. The pipeline-start preflight
	// refuses to launch a run when the GitHub REST/GraphQL bucket is below
	// MIN_RATE_LIMIT_HEADROOM, which — pre-fix — burned the issue at
	// pipeline-start (observed: acmeapp #15/#41 dispatched at 8/5000). Catch
	// it HERE instead: when the bucket (read for free from the shared tracker)
	// is known-low, set a global cooldown until the bucket resets and defer.
	// The existing quotaCooldownActiveLocked gate above then suspends dispatch
	// until reset — no pipeline is started, so no issue is failed. An UNKNOWN
	// reading (tracker empty / no observation yet) never blocks.
	// A low reading only blocks while it is still CURRENT — i.e. its reset
	// window has not already elapsed. A low `remaining` whose `resetAt` is in
	// the past is stale (the hourly bucket has since refilled) and must not
	// gate, or the scheduler could defer in a 1-minute loop on an old reading
	// that no gh call has refreshed because the defer itself suppresses the
	// graph build that would refresh it.
	if remaining, limit, resetAt, ok := as.gitHubQuotaSnapshot(); ok && remaining >= 0 &&
		remaining < minGitHubQuotaHeadroom && time.Now().Before(resetAt) {
		as.state.LastScanAt = time.Now().UTC().Format(time.RFC3339)
		as.state.LastRejectionReasons = map[string]int{"github-quota-low": 1}
		as.state.LastCandidateCount = 0
		as.applyGitHubQuotaCooldownLocked(resetAt,
			fmt.Sprintf("%d/%d remaining, need ≥%d", remaining, limit, minGitHubQuotaHeadroom))
		as.mu.Unlock()
		log.Printf("autonomous: skipping dispatch — GitHub API quota low (%d remaining, need ≥%d), cooldown until %s",
			remaining, minGitHubQuotaHeadroom, resetAt.UTC().Format(time.RFC3339))
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	as.state.CyclesRun++
	as.state.LastScanAt = time.Now().UTC().Format(time.RFC3339)
	as.mu.Unlock()

	// 1. Gate on slot availability BEFORE building the graph.
	// Building the graph costs GraphQL quota. When no effective slots remain
	// (considering both the global cap and any per-repo caps), skip the build
	// entirely. When a pipeline finishes, TriggerRescan fires immediately.
	availableSlots := as.effectiveAvailableSlots()
	if availableSlots <= 0 {
		as.mu.Lock()
		log.Printf("autonomous: no effective slots available (MaxConcurrent=%d, running=%d), skipping graph build",
			as.config.MaxConcurrent, len(as.state.Running))
		as.mu.Unlock()
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	// 2. Build/refresh the cross-repo dependency graph (with TTL cache)
	ttl := as.config.GraphCacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	as.mu.Lock()
	cached := as.graphCache
	cachedAt := as.graphCacheAt
	as.mu.Unlock()

	var graph *depgraph.Graph
	graphWasFresh := false
	if cached != nil && time.Since(cachedAt) < ttl {
		log.Printf("autonomous: graph cache hit (age=%s, TTL=%s, nodes=%d)",
			time.Since(cachedAt).Round(time.Second), ttl, len(cached.Nodes))
		graph = cached
	} else {
		graphWasFresh = true
		var buildErr error
		graph, buildErr = as.buildGraphFn(ctx)
		if buildErr != nil {
			log.Printf("autonomous: graph build failed: %v", buildErr)
			return
		}
		log.Printf("autonomous: graph built fresh: %d nodes, scanning %d repos",
			len(graph.Nodes), len(as.repos))
		logIncompleteGraph(graph)
		as.mu.Lock()
		as.graphCache = graph
		as.graphCacheAt = time.Now()
		as.mu.Unlock()
	}

	// 2b. Reconcile completed/failed lists against live GitHub state.
	// Items may have been recorded as "completed" or "failed" in a previous
	// session but the issue is still OPEN on GitHub (e.g., due to a cross-repo
	// routing bug that prevented closing). Re-admit them as candidates.
	as.reconcileStateAgainstGraph(graph)

	// 2c. Recover PRs left "sitting" in review — a failed/stale pr-merge leaves
	// the issue in "In review" where it is neither dispatchable nor blocking, so
	// it deadlocks its epic. Move verifiably-stuck (OPEN + BEHIND/DIRTY) PRs back
	// to Ready so pr-merge re-runs and freshens/resolves the branch (#3894).
	// Only on a FRESH graph build (#3896): the sweep makes one gh-pr-list call
	// per repo, so gating it to the graph TTL (instead of every cycle) avoids
	// draining the GitHub quota that the pipeline-start preflight depends on.
	if graphWasFresh {
		as.reconcileStuckInReviewPRs(ctx, graph)
		// Refresh the "open PR is BLOCKED" set consumed by prioritize() below.
		// Gated to fresh builds (one gh-pr-list per repo) for the same quota
		// reason as the in-review sweep; prioritize() reads the cached set every
		// cycle without any GitHub call. Ends the failed-pr-merge → revert-to-
		// Ready → full-re-run churn for PRs a human must unblock.
		as.refreshBlockedReadyPRs(ctx, graph)
	}

	// 2d. (#4151) Finalize due post-merge survival records — poll-on-reconcile,
	// no new cron (spike #4134 §1.4). Gated to FRESH graph builds: like the
	// in-review sweep, survival detection makes per-record GitHub calls, so we
	// run it at the graph TTL cadence rather than every cycle to protect the
	// shared GitHub quota. Best-effort and non-blocking.
	if graphWasFresh {
		as.sweepSurvivalRecords(ctx)
	}

	// 3. Re-check effective slots after graph build — a pipeline may have
	// completed while we were fetching.
	availableSlots = as.effectiveAvailableSlots()
	if availableSlots <= 0 {
		as.mu.Lock()
		log.Printf("autonomous: all slots occupied after graph build (MaxConcurrent=%d, running=%d), waiting",
			as.config.MaxConcurrent, len(as.state.Running))
		as.mu.Unlock()
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	// 4. Prioritize: unblocked, open items sorted by priority rules
	candidates := as.prioritize(ctx, graph)
	log.Printf("autonomous: %d candidates from %d nodes", len(candidates), len(graph.Nodes))

	// 5. Update remaining count
	as.mu.Lock()
	as.state.Remaining = len(candidates)
	as.mu.Unlock()

	// 6. Fill slots with top candidates (safety-checked)
	dispatched := 0
	for i := 0; i < len(candidates) && dispatched < availableSlots; i++ {
		// Stop-aware bail-out: if Stop() was called since this cycle began,
		// abort the rest of the dispatch loop. Without this, the user's
		// "Stop Autonomous" would not take effect until after every
		// candidate in the already-built list had been dispatched via
		// onDispatch — leaking autonomous.dispatch events to the TypeScript
		// side and re-populating the queue the user just cleared.
		//
		// #3020 follow-up: Pause()/safety_tripped have the same problem.
		// haltQueueOnSlotFailure pauses autonomous AFTER a pipeline failure,
		// but if a runCycle was already mid-flight when Pause() ran, the
		// loop dispatches one more issue before the next tick re-reads
		// state.Status. Observed: #291 stall-killed at 13:25:15, autonomous
		// paused, then #785 dispatched at 13:25:38 — same cycle. Re-check
		// state.Status per-candidate so a Pause mid-cycle takes effect
		// immediately.
		as.mu.Lock()
		stopRequested := as.stopRequested
		statusNow := as.state.Status
		as.mu.Unlock()
		if stopRequested {
			log.Printf("autonomous: stop requested mid-cycle — skipping %d remaining dispatches", len(candidates)-i)
			break
		}
		if statusNow != "running" {
			log.Printf("autonomous: status changed mid-cycle to %q — skipping %d remaining dispatches",
				statusNow, len(candidates)-i)
			break
		}

		item := candidates[i]
		// Skip items already running
		if as.isRunning(item.Repo, item.Number) {
			continue
		}

		// Skip if repo is at its per-repo concurrency cap. The cap is the
		// numeric per-repo limit when set, falling back to 1 for the legacy
		// boolean sequential flag, and 0 (no cap) otherwise.
		if cap := as.maxConcurrentForRepo(item.Repo); cap > 0 {
			if as.runningCountFrom(item.Repo) >= cap {
				log.Printf("autonomous: skipping %s#%d — repo at cap (%d running, max %d)",
					item.Repo, item.Number, as.runningCountFrom(item.Repo), cap)
				continue
			}
		}

		// #3020: Per-issue terminal-failure cap. If this issue has already
		// failed MaxLifetimeFailuresPerIssue times across all sessions, refuse
		// to dispatch and trip safety mode so the user must triage. Without
		// this guard, a chronically-broken issue (like #283 in the original
		// incident) could be retried indefinitely after each Resume() — burning
		// $20+ per retry until the global circuit breaker eventually catches it.
		key := fmt.Sprintf("%s#%d", item.Repo, item.Number)
		as.mu.Lock()
		lifetimeFails := 0
		if as.state.LifetimeIssueFailures != nil {
			lifetimeFails = as.state.LifetimeIssueFailures[key]
		}
		as.mu.Unlock()
		if lifetimeFails >= MaxLifetimeFailuresPerIssue {
			log.Printf("autonomous: skipping %s#%d — exceeded lifetime failure cap (%d/%d), manual triage required",
				item.Repo, item.Number, lifetimeFails, MaxLifetimeFailuresPerIssue)
			as.mu.Lock()
			as.state.Status = "safety_tripped"
			if as.state.Safety == nil {
				as.state.Safety = &SafetyState{}
			}
			tripReason := fmt.Sprintf(
				"issue %s has failed %d times — manual triage required (clear via clearIssueFailures or remove from board)",
				key, lifetimeFails)
			as.state.Safety.TripReason = tripReason
			as.state.PauseReason = tripReason
			as.state.PauseTriggeredBy = "safety:lifetime-failure-cap"
			as.state.PausedAt = time.Now().UTC().Format(time.RFC3339)
			as.fireStatusChangeLocked()
			as.mu.Unlock()
			as.persistState()
			if as.onCycleComplete != nil {
				as.onCycleComplete()
			}
			return
		}

		if as.config.DryRun {
			log.Printf("autonomous [dry-run]: would enqueue %s#%d (%s)", item.Repo, item.Number, item.Title)
			continue
		}

		// GraphQL dispatch headroom check. A pipeline run typically consumes
		// ~1500-2000 GraphQL requests across its stages — board reads, status
		// mutations, PR queries, sub-issue completion checks, etc. Without a
		// pre-dispatch gate, the scheduler will happily start a new pipeline
		// even when remaining=200, blowing through the bucket within minutes
		// and tripping the rate-limit circuit breaker. The per-call floor
		// (default 100) is too low to prevent this — by the time it engages,
		// we've already burned the budget. This check reads the cached tracker
		// state (free) and defers the dispatch loop when headroom is too low.
		// The pre-dispatch gate stops only the loop iteration that would
		// exceed budget; pipelines already in flight continue undisturbed.
		if ok, reason := as.hasDispatchHeadroom(); !ok {
			log.Printf("autonomous: deferring dispatch of %s#%d — %s",
				item.Repo, item.Number, reason)
			as.mu.Lock()
			if as.state.LastRejectionReasons == nil {
				as.state.LastRejectionReasons = make(map[string]int)
			}
			as.state.LastRejectionReasons["github-rate-limit-headroom"]++
			as.mu.Unlock()
			break
		}

		// Safety rail check before each enqueue
		if as.safetyRails != nil {
			allowed, reason := as.safetyRails.CheckBeforeEnqueue(0)
			if !allowed {
				log.Printf("autonomous: safety rail blocked enqueue of %s#%d: %s",
					item.Repo, item.Number, reason)
				as.mu.Lock()
				as.state.Status = "safety_tripped"
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
				as.state.PauseReason = reason
				as.state.PauseTriggeredBy = "safety:rail-check"
				as.state.PausedAt = time.Now().UTC().Format(time.RFC3339)
				as.fireStatusChangeLocked()
				as.mu.Unlock()
				as.persistState()
				if as.onCycleComplete != nil {
					as.onCycleComplete()
				}
				return
			}
			as.safetyRails.RecordPipelineStart()
		}

		as.enqueueItem(ctx, item)
		dispatched++
	}

	// 6. Check budget (legacy check — safety rails also enforce this)
	as.mu.Lock()
	if as.state.TokensCeiling > 0 && as.state.TokensSpent >= as.state.TokensCeiling {
		as.mu.Unlock()
		as.complete("budget_exhausted")
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	// 7. Log idle state but keep scanning — new issues may appear on any
	// repo board at any time. The scheduler only terminates via explicit
	// Stop(), budget exhaustion, or safety trip.
	remaining := as.state.Remaining
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if remaining == 0 && runningCount == 0 {
		log.Printf("autonomous: idle — no candidates or running pipelines, will re-scan next cycle")
		// No-silent-stall watchdog (#4073): an idle cycle is exactly when an epic
		// with open-but-blocked sub-issues and a silently-failed merge looks
		// identical to "done". Surface any such epic with the blocking reason.
		as.surfaceStuckEpics(ctx, graph)
		// Action Center work-exhaustion producer (ADR 015 §F #1): the fleet is
		// idle with nothing dispatchable — the motivating incident. Raise a
		// fleet-scoped card so the operator can re-scan/promote from any surface
		// instead of learning of it from a bare one-way "stopped" notice.
		as.mu.Lock()
		promotable := 0
		for _, n := range as.state.LastRejectionReasons {
			promotable += n
		}
		as.mu.Unlock()
		as.raiseWorkExhaustion(promotable)
	}

	// Action Center expiry sweep (ADR 015 §C): piggyback the periodic scan so no
	// DecisionRequest lingers past its expires_at.
	as.sweepAttentionExpired(ctx)

	as.persistState()
	if as.onCycleComplete != nil {
		as.onCycleComplete()
	}
}

// CandidateItem is a prioritized item ready for dispatch.
type CandidateItem struct {
	Repo         string
	Number       int
	Title        string
	Priority     string   // P0, P1, P2, P3
	Size         string   // XS, S, M, L, XL
	Labels       []string // issue labels for focus keyword matching
	BoardStatus  string   // project board status: Ready, Backlog, In progress, etc.
	OnCritPath   bool
	UnblockCount int // how many downstream items this unblocks
}

// isWorkCompleteStatus returns true when the dep's pipeline work is
// effectively done — even if the issue itself is still OPEN. Currently this
// covers "In review" (PR open, awaiting merge). Downstream items are not
// blocked by deps in these statuses because the upstream output (the PR /
// merged code) already exists. If the PR is later rejected, the issue moves
// back to In progress / Backlog and the dep re-blocks on the next scan.
func isWorkCompleteStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "in review"
}

// isReadyStatus returns true when the board status indicates the issue is
// explicitly approved for pipeline dispatch. Case-insensitive match on
// common "ready" variations (Ready, Todo, To Do).
func isReadyStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "ready" || s == "todo" || s == "to do"
}

// isBacklogStatus returns true for issues in the Backlog column. These are
// only dispatched when pickup_backlog is enabled AND all Ready items for
// the repo have been processed.
func isBacklogStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "backlog")
}

// isDispatchableStatus returns true for board statuses eligible for autonomous
// dispatch. Issues in "In progress", "In review", "Done", or empty status are
// never dispatched — they are either already being worked on, completed, or
// not yet triaged onto the board.
func isDispatchableStatus(status string, pickupBacklog bool) bool {
	if isReadyStatus(status) {
		return true
	}
	if pickupBacklog && isBacklogStatus(status) {
		return true
	}
	return false
}

// focusAlignmentScore computes a numerical boost (0–20) based on how well an
// issue's labels and title match the active focus lens keywords. Returns 0
// when the lens is nil or "general" (no boost), ensuring backward compatibility.
func (as *AutonomousScheduler) focusAlignmentScore(item *CandidateItem, activeLens *focus.Lens) int {
	if activeLens == nil || activeLens.Name == "general" {
		return 0
	}

	score := 0
	lensKeywords := activeLens.Keywords

	// +2 per keyword match in labels
	for _, label := range item.Labels {
		labelLower := strings.ToLower(label)
		for _, kw := range lensKeywords {
			if strings.Contains(labelLower, strings.ToLower(kw)) {
				score += 2
			}
		}
	}

	// +1 per keyword match in title
	titleLower := strings.ToLower(item.Title)
	for _, kw := range lensKeywords {
		if strings.Contains(titleLower, strings.ToLower(kw)) {
			score++
		}
	}

	if score > 20 {
		score = 20
	}
	return score
}

// rawAdjacency returns the outgoing dependency adjacency list (From -> []To)
// built from ALL edges in g.Edges whose From is a real graph node — including
// edges whose To is NOT a node (a "dangling" dependency reference: an issue,
// commonly an epic, that was never added to any project board — see #306).
//
// This is deliberately more permissive than Graph.Adjacency(), which silently
// drops such edges. That filtering is correct for topology (Waves /
// CriticalPath — a node that isn't in the graph can't be topologically
// ordered) but is the WRONG behavior for candidate-selection dep checks: they
// must distinguish "no dependency" from "dependency of unknown state" so the
// latter can be resolved and, if still unresolved, fail closed — instead of
// the edge just vanishing and the dep reading as silently satisfied.
func rawAdjacency(g *depgraph.Graph) map[string][]string {
	adj := make(map[string][]string, len(g.Edges))
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		adj[fromKey] = append(adj[fromKey], g.NodeKey(e.To))
	}
	return adj
}

// depBlockResult is the outcome of scanning a node's outgoing dependency
// edges for the first unsatisfied blocker.
type depBlockResult struct {
	blocked  bool
	blocker  string // depKey of the blocking dependency, set only if blocked
	status   string // human-readable status for the log line
	offBoard bool   // true when the block came from a dep with no node in the
	// graph (resolved — or left unresolved — via the issue service instead)
}

// evaluateDeps scans depKeys — a node's outgoing dependency edges from
// rawAdjacency — and reports whether any of them still blocks dispatch.
//
// A dep with a matching node in g.Nodes is evaluated exactly as before this
// fix: OPEN and not work-complete == blocking.
//
// A dep with NO node in g.Nodes is "dangling" (see rawAdjacency) and is
// resolved against `resolved`, the batch lookup populated once per
// prioritize() pass by resolveIssueStatesByKey:
//   - resolved[depKey] == "CLOSED" -> satisfied; keep scanning other deps.
//   - resolved[depKey] == "OPEN"   -> BLOCKED. The blocker is real and open,
//     it just isn't board-synced.
//   - depKey absent from resolved  -> BLOCKED (fail closed). Absence means no
//     issue-state resolver was available, the GitHub lookup for its repo
//     failed, or the batch response simply omitted it — never guess
//     "satisfied" for a blocker of unknown state (#306).
func evaluateDeps(depKeys []string, g *depgraph.Graph, resolved map[string]string) depBlockResult {
	for _, depKey := range depKeys {
		depNode, exists := g.Nodes[depKey]
		if !exists {
			state, ok := resolved[depKey]
			switch {
			case !ok:
				return depBlockResult{
					blocked: true, blocker: depKey, offBoard: true,
					status: "unresolved (not on any project board and not confirmed via GitHub — failing closed)",
				}
			case strings.EqualFold(state, "OPEN"):
				return depBlockResult{
					blocked: true, blocker: depKey, offBoard: true,
					status: "OPEN (resolved via GitHub — not on any project board)",
				}
			default:
				continue // resolved CLOSED -> satisfied, keep scanning
			}
		}
		if !strings.EqualFold(depNode.State, "OPEN") {
			continue
		}
		if isWorkCompleteStatus(depNode.BoardStatus) {
			continue
		}
		return depBlockResult{blocked: true, blocker: depKey, status: depNode.BoardStatus}
	}
	return depBlockResult{}
}

// prioritize selects and orders open, unblocked items from the graph.
//
// Board status gating (eliminates the race condition where issues are picked
// up before blockedBy relationships are applied):
//   - Only "Ready" (and "Todo"/"To Do") items are dispatched by default
//   - "Backlog" items are dispatched only when pickup_backlog is true AND
//     all Ready items for the same repo have been dispatched/completed
//   - "In progress", "In review", "Done", and empty-status items are never dispatched
//
// Sort order: board status (Ready > Backlog) > critical path > focus alignment
// > priority > smaller size > higher unblock count.
func (as *AutonomousScheduler) prioritize(ctx context.Context, g *depgraph.Graph) []CandidateItem {
	if g == nil || len(g.Nodes) == 0 {
		return nil
	}

	// Load active focus lens for alignment scoring. Gracefully falls back to
	// "general" (no boost) when focus.yaml is missing or unreadable.
	focusManager := focus.NewManager(as.workspaceRoot)
	focusState, _ := focusManager.Load() // error → default "general" state
	var activeLens *focus.Lens
	if focusState != nil {
		activeLens = focusManager.ResolveLens(focusState.ActiveLens, focusState)
	}
	if activeLens != nil && activeLens.Name != "general" {
		log.Printf("autonomous: focus lens active=%q (keywords: %v)", activeLens.Name, activeLens.Keywords)
	}

	// Build sets for fast lookup
	critPathSet := make(map[string]bool)
	for _, id := range g.CriticalPath {
		critPathSet[g.NodeKey(id)] = true
	}

	// Reverse adjacency: for each node, count how many nodes it blocks.
	// Edge semantics: From depends on To, so completing To unblocks From.
	// reverseAdj gives To -> []From, i.e. "what does completing this node unlock?"
	revAdj := g.ReverseAdjacency()

	// Running set for filtering
	as.mu.Lock()
	runningSet := make(map[string]bool)
	for _, r := range as.state.Running {
		runningSet[fmt.Sprintf("%s#%d", r.Repo, r.Number)] = true
	}
	completedSet := make(map[string]bool)
	for _, c := range as.state.Completed {
		completedSet[fmt.Sprintf("%s#%d", c.Repo, c.Number)] = true
	}
	// Snapshot backoff map under the lock so we don't hold it during the loop.
	backoffSnapshot := make(map[string]time.Time, len(as.retryBackoff))
	for k, v := range as.retryBackoff {
		backoffSnapshot[k] = v
	}
	// Capture the "open PR is BLOCKED" set (refreshed on fresh builds by
	// refreshBlockedReadyPRs). The map is replaced wholesale, never mutated in
	// place, so a captured reference is stable for this pass; a nil map reads as
	// all-false. No GitHub call happens here — the guard is pure map lookup.
	blockedPRSet := as.blockedReadyPRIssues
	as.mu.Unlock()

	now := time.Now()

	// Adjacency: From -> []To (what does this node depend on?). Raw (i.e.
	// NOT g.Adjacency()) so dangling dependency edges — pointing at an issue
	// with no node in the graph, because the graph only creates nodes from
	// project-board items — are still visible here instead of silently
	// vanishing. g.Adjacency() drops those edges entirely, which is correct
	// for topology (an absent node can't be topologically ordered) but was
	// exactly how an off-board blocker went unnoticed and let a blocked issue
	// dispatch (#306).
	adj := rawAdjacency(g)

	// Collect every dangling dep key referenced anywhere in adj — both the
	// direct blockedBy check and the epic-cascade check below read from this
	// same map, keyed by each edge's own From, so one scan over its values
	// finds all of them regardless of which check will consume them — and
	// batch-resolve their true GitHub state ONCE for this pass. This mirrors
	// the per-repo batching discipline of refreshBlockerStates (scheduler.go):
	// N candidates that all share one off-board blocker cost a single lookup,
	// not N. The cache is local to this prioritize() call only — scoped to the
	// current scan/tick, never held across cycles, since blocker state can
	// change between scans.
	danglingDepKeys := make(map[string]bool)
	for _, targets := range adj {
		for _, t := range targets {
			if _, ok := g.Nodes[t]; !ok {
				danglingDepKeys[t] = true
			}
		}
	}
	resolvedDepStates := map[string]string{}
	if len(danglingDepKeys) > 0 {
		keys := make([]string, 0, len(danglingDepKeys))
		for k := range danglingDepKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if as.resolveDepStatesFn != nil {
			if r := as.resolveDepStatesFn(ctx, keys); r != nil {
				resolvedDepStates = r
			}
			log.Printf("autonomous: resolved %d/%d off-board dependency key(s) not on any project board: %v",
				len(resolvedDepStates), len(keys), keys)
		} else {
			log.Printf("autonomous: WARNING no issue-state resolver wired — %d off-board dependency key(s) will fail closed (treated as blocked): %v",
				len(keys), keys)
		}
	}

	// Build an allowed-repos set from as.repos. When non-empty, only nodes
	// belonging to those repos are dispatched. Cross-repo edges are still
	// evaluated for blocking purposes (the full graph is always built).
	allowedRepos := make(map[string]bool, len(as.repos))
	for _, rc := range as.repos {
		allowedRepos[rc.FullName()] = true
	}

	// Per-reason rejection counts so we can answer "why 0 candidates from N
	// nodes?" in one log line instead of guessing. Without this, the scheduler
	// silently drops items at multiple gates and the only debugging path is to
	// add ad-hoc prints. See #fix-zero-candidates-blackbox.
	rejected := map[string]int{}
	bump := func(reason string) { rejected[reason]++ }

	var candidates []CandidateItem
	for key, node := range g.Nodes {
		// Skip nodes not in the filtered repo set (--repos restriction).
		// The full graph is still built from all repos so cross-repo blocking
		// edges are evaluated; we only restrict which nodes are dispatched.
		if len(allowedRepos) > 0 && !allowedRepos[node.Repo] {
			bump("repo-not-in-filter")
			continue
		}

		// Skip closed issues
		if strings.EqualFold(node.State, "CLOSED") {
			bump("closed")
			continue
		}

		// Skip already running or completed
		if runningSet[key] {
			bump("already-running")
			continue
		}
		if completedSet[key] {
			bump("already-completed")
			continue
		}

		// Skip issues still within their per-issue exponential backoff window.
		if retryAt, inBackoff := backoffSnapshot[key]; inBackoff && now.Before(retryAt) {
			log.Printf("autonomous: skipping %s (backoff until %s, %.0fs remaining)",
				key, retryAt.UTC().Format(time.RFC3339),
				retryAt.Sub(now).Seconds())
			bump("backoff")
			continue
		}

		// Skip items with type:epic label (epics are tracked, not dispatched directly)
		isEpic := false
		for _, label := range node.Labels {
			if strings.EqualFold(label, "type:epic") {
				isEpic = true
				break
			}
		}
		if isEpic {
			bump("epic")
			continue
		}

		// Skip issues carrying a human-only label (autonomous.exclude_labels,
		// default ["owner-action"]) — work only a human operator can do (e.g.
		// rotating a cloud credential in a provider dashboard). Dispatching
		// these burns tokens through issue-pickup → planning → feature-dev →
		// validate and then fails at pr-create with nothing to commit (#317).
		// Matched case-insensitively, mirroring the type:epic check above.
		if label, excluded := excludedLabelMatch(node.Labels, resolvedExcludeLabels(as.config.ExcludeLabels)); excluded {
			bump("excluded-label:" + strings.ToLower(label))
			log.Printf("autonomous: skipping %s — carries human-only label %q (autonomous.exclude_labels); needs a human, no pipeline retry can clear it", key, label)
			// Action Center owner-action handoff producer (ADR 015 §F #2): the
			// skip is otherwise silent (#320). Raise a handoff card with the
			// checklist + mark-done-and-requeue-dependents. Dedup on issue key
			// keeps re-detection from spawning duplicates.
			as.raiseOwnerActionHandoff(node.Repo, node.Number, node.Title, label)
			continue
		}

		// Board status gate: only dispatch items whose project board status is
		// explicitly set to "Ready" (or "Backlog" when pickup_backlog is enabled).
		// This eliminates the race condition where issues are dispatched before
		// blockedBy relationships are configured.
		if !isDispatchableStatus(node.BoardStatus, as.config.PickupBacklog) {
			bump(fmt.Sprintf("status=%q", node.BoardStatus))
			continue
		}

		// Skip issues whose OPEN PR is BLOCKED (a failing required check or
		// branch-protection rule). The work already shipped a PR; re-running the
		// entire pipeline can't clear a repo-config block — only a human can, by
		// fixing the check or the required-checks config. Without this guard a
		// failed pr-merge reverts the issue to Ready and it re-runs every cycle,
		// burning tokens on a PR that still can't merge. The set is refreshed on
		// fresh graph builds (refreshBlockedReadyPRs); this is a pure lookup, no
		// GitHub call. Self-healing: once the PR unblocks/merges/closes it leaves
		// the set on the next fresh scan and the issue is eligible again.
		if blockedPRSet[key] {
			bump("open-pr-blocked")
			log.Printf("autonomous: skipping %s — open PR is BLOCKED (required check / branch protection; needs human, no retry can clear)", key)
			continue
		}

		// Check if all dependencies are satisfied. A dep blocks dispatch only
		// when it is OPEN and its work is not yet effectively complete. "In
		// review" means the PR is open / merging — the upstream work is done,
		// so downstream items can start. (If the PR is later rejected, the
		// issue moves back to In progress / Backlog and the dep re-blocks on
		// the next scan — self-healing.) "Done" is already CLOSED so it never
		// reaches this branch.
		blocked := false
		var blocker, blockerStatus string
		offBoard := false
		if res := evaluateDeps(adj[key], g, resolvedDepStates); res.blocked {
			blocked = true
			blocker = res.blocker
			blockerStatus = res.status
			offBoard = res.offBoard
		}
		if blocked {
			reason := "blocked-by-open-dep"
			if offBoard {
				reason = "blocked-by-offboard-dep"
				log.Printf("autonomous: blocked %s by off-board dep %s (%s) — this blocker is not on any project board; fix board hygiene (add it to a board, or correct/remove the blockedBy edge)",
					key, blocker, blockerStatus)
			} else {
				log.Printf("autonomous: blocked %s by open dep %s (status=%q)",
					key, blocker, blockerStatus)
			}
			bump(reason)
			continue
		}

		// Epic-level cascade: if this node's parent epic has an open blockedBy,
		// treat the sub-issue as blocked too. This prevents out-of-order
		// execution when epics are wired with blockedBy dependencies but their
		// sub-issues have no individual blockers. Opt-out via
		// DisableEpicBlockedByCascade in AutonomousConfig.
		if !as.config.DisableEpicBlockedByCascade && node.EpicNumber != 0 {
			epicKey := g.NodeKey(depgraph.NodeID{Repo: node.Repo, Number: node.EpicNumber})
			if epicNode, ok := g.Nodes[epicKey]; ok && strings.EqualFold(epicNode.State, "OPEN") {
				if res := evaluateDeps(adj[epicKey], g, resolvedDepStates); res.blocked {
					blocked = true
					blocker = res.blocker
					offBoard = res.offBoard
					prefix := "(via epic #" + strconv.Itoa(node.EpicNumber) + ") "
					if res.offBoard {
						prefix = "(via epic #" + strconv.Itoa(node.EpicNumber) + ", off-board) "
					}
					blockerStatus = prefix + res.status
				}
			}
		}
		if blocked {
			reason := "blocked-by-epic-dep"
			if offBoard {
				reason = "blocked-by-offboard-epic-dep"
			}
			bump(reason)
			log.Printf("autonomous: blocked %s by epic dep %s (status=%q)",
				key, blocker, blockerStatus)
			continue
		}

		// Count downstream unblocks
		unblockCount := len(revAdj[key])

		candidates = append(candidates, CandidateItem{
			Repo:         node.Repo,
			Number:       node.Number,
			Title:        node.Title,
			Priority:     node.Priority,
			Size:         node.Size,
			Labels:       node.Labels,
			BoardStatus:  node.BoardStatus,
			OnCritPath:   critPathSet[key],
			UnblockCount: unblockCount,
		})
	}

	// Dangling epic gate warning: when cascade is disabled, warn if an epic has
	// an open blockedBy but one or more of its sub-issues became candidates.
	// When cascade is enabled this situation is structurally impossible.
	if as.config.DisableEpicBlockedByCascade {
		candidateSet := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			candidateSet[fmt.Sprintf("%s#%d", c.Repo, c.Number)] = true
		}
		for _, node := range g.Nodes {
			if node.EpicNumber != 0 {
				continue // only inspect epics, not sub-issues
			}
			isEpicNode := false
			for _, label := range node.Labels {
				if strings.EqualFold(label, "type:epic") {
					isEpicNode = true
					break
				}
			}
			if !isEpicNode || !strings.EqualFold(node.State, "OPEN") {
				continue
			}
			epicKey := g.NodeKey(node.ID())
			hasOpenBlocker := evaluateDeps(adj[epicKey], g, resolvedDepStates).blocked
			if !hasOpenBlocker {
				continue
			}
			// Find candidates that are sub-issues of this epic (same repo).
			var danglingKeys []string
			for _, c := range candidates {
				subKey := fmt.Sprintf("%s#%d", c.Repo, c.Number)
				if !candidateSet[subKey] {
					continue
				}
				subNode, ok := g.Nodes[subKey]
				if ok && subNode.EpicNumber == node.Number && subNode.Repo == node.Repo {
					danglingKeys = append(danglingKeys, subKey)
				}
			}
			if len(danglingKeys) > 0 {
				log.Printf("autonomous: WARNING dangling epic gate: epic %s has open blockedBy but sub-issues are schedulable: %v",
					epicKey, danglingKeys)
			}
		}
	}

	// Persist + log the rejection breakdown so 0-candidate cycles are never
	// opaque again. The summary always logs (even with non-zero candidates) so
	// the user can answer "why isn't issue X being picked up?" without
	// instrumenting the binary every time.
	as.mu.Lock()
	as.state.LastRejectionReasons = rejected
	as.state.LastCandidateCount = len(candidates)
	as.state.LastNodeCount = len(g.Nodes)
	as.mu.Unlock()
	if len(rejected) > 0 {
		// Stable, sorted output: "blocked-by-open-dep=4, epic=8, status=\"In progress\"=2"
		keys := make([]string, 0, len(rejected))
		for k := range rejected {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, rejected[k]))
		}
		log.Printf("autonomous: prioritize rejected %d/%d nodes: %s",
			len(g.Nodes)-len(candidates), len(g.Nodes), strings.Join(parts, ", "))
	}

	// Sort by priority rules:
	// 0. Board status (Ready > Backlog — Ready always dispatched first)
	// 1. Higher priority (P0 > P1 > P2 > P3) — explicit priority dominates
	//    everything below. A P0 item sitting Ready MUST be picked before a
	//    P1 item, regardless of how favorable that P1 looks on the critical
	//    path or focus lens. Issue #3396: GitLab forge sub-issues (#3349
	//    epic) form a long sequential chain → every sub-issue ends up "on
	//    the critical path" → P1 GitLab work was dispatching while standalone
	//    P0 items (audit-log 404, telemetry consent) sat Ready for hours.
	// 2. Critical path items first (within the same priority)
	// 3. Focus alignment (higher boost = higher priority within non-crit items)
	// 4. Smaller size first (faster unblock cycle)
	// 5. Higher unblock count (items that unblock the most downstream work)
	sort.Slice(candidates, func(i, j int) bool {
		// 0. Board status: Ready items always before Backlog items
		iReady := isReadyStatus(candidates[i].BoardStatus)
		jReady := isReadyStatus(candidates[j].BoardStatus)
		if iReady != jReady {
			return iReady // Ready items first
		}
		// 1. Priority — explicit P0/P1/P2/P3 label dominates all heuristics below.
		pi := candidatePriorityRank(candidates[i].Priority)
		pj := candidatePriorityRank(candidates[j].Priority)
		if pi != pj {
			return pi < pj // lower rank = higher priority
		}
		// 2. Critical path (within the same priority)
		if candidates[i].OnCritPath != candidates[j].OnCritPath {
			return candidates[i].OnCritPath
		}
		// 3. Focus alignment
		boostI := as.focusAlignmentScore(&candidates[i], activeLens)
		boostJ := as.focusAlignmentScore(&candidates[j], activeLens)
		if boostI != boostJ {
			return boostI > boostJ // higher boost first
		}
		// 4. Size (smaller first)
		si := depgraph.SizeWeight(candidates[i].Size)
		sj := depgraph.SizeWeight(candidates[j].Size)
		if si != sj {
			return si < sj
		}
		// 5. Unblock count (higher first)
		if candidates[i].UnblockCount != candidates[j].UnblockCount {
			return candidates[i].UnblockCount > candidates[j].UnblockCount
		}
		// Tie-break: lower issue number (older first)
		return candidates[i].Number < candidates[j].Number
	})

	return candidates
}

// candidatePriorityRank maps priority string to a numeric rank (lower = higher priority).
func candidatePriorityRank(p string) int {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

// enqueueItem adds an item to the existing scheduler's queue and starts it.
func (as *AutonomousScheduler) enqueueItem(ctx context.Context, item CandidateItem) {
	log.Printf("autonomous: dispatching %s#%d (%s)", item.Repo, item.Number, item.Title)

	// Add to running set
	as.mu.Lock()
	as.state.Running = append(as.state.Running, RunningItem{
		Repo:      item.Repo,
		Number:    item.Number,
		Title:     item.Title,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	as.mu.Unlock()
	as.persistState()

	// Route through the Dispatcher interface when set (preferred path).
	// Falls back to the legacy onDispatch callback or Go scheduler queue.
	if as.dispatcher != nil {
		if _, err := as.dispatcher.Dispatch(ctx, item); err != nil {
			log.Printf("autonomous: dispatcher error for %s#%d: %v", item.Repo, item.Number, err)
		}
		return
	}

	// Legacy path: dispatch via callback (IPC → TypeScript HeadlessOrchestrator)
	// when available, otherwise enqueue in Go scheduler queue for CLI-only mode.
	ownerPart, repoPart := splitOwnerRepo(item.Repo)
	if as.onDispatch != nil {
		as.onDispatch(ownerPart, repoPart, item.Number, item.Title)
	} else {
		as.scheduler.QueueAdd(QueueEntry{
			Repo:        repoPart,
			IssueNumber: item.Number,
			Priority:    candidatePriorityRank(item.Priority),
		})
		go func() {
			if err := as.scheduler.RunQueue(ctx); err != nil {
				log.Printf("autonomous: queue processing error for %s#%d: %v",
					item.Repo, item.Number, err)
			}
		}()
	}
}

// onPipelineComplete is called when a pipeline run finishes.
// It updates state, records the outcome with safety rails, and triggers an
// immediate re-scan for cascade unblocks.
//
// conflictRestart is the LEGACY fresh-branch conflict-restart signal. As of
// #4072 the primary conflict path is the branch-preserving conflict-recovery
// loop, which resolves the conflict WITHIN a single pipeline run by rewinding to
// feature-dev (scheduler.go) — pr-merge no longer emits conflict-restart-{N}.json
// for the recoverable case, so the TypeScript layer no longer force-deletes the
// remote branch and conflictRestart arrives false. When conflictRestart IS true
// (a residual fresh-branch signal), the circuit breaker is still NOT incremented
// for the first MaxConflictRestarts attempts, after which the issue falls
// through to regular failure handling. Either way the branch is preserved for
// the dev re-dispatch unless an explicit fresh-branch signal is present.
//
// failureDetail is the raw failure text (kill marker, error envelope) used to
// extract `resetsAt=<unix>` for quota-exhausted failures so the global
// Anthropic-quota cooldown runs until the actual bucket reset (#3431).
// Optional — empty falls back to a 1-hour floor.
func (as *AutonomousScheduler) onPipelineComplete(repo string, issue int, success bool, conflictRestart bool, terminalFailureKind string, failureDetail string) {
	as.mu.Lock()
	defer as.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)

	// Cascade trip outcome (#3605 bullet C). Hoisted to function scope so the
	// safety-rails block at the end of the function can preserve the cascade
	// trip reason on the persisted Safety state. Set inside the failure
	// branch below; otherwise stays zero-valued and the safety-rails block
	// is a no-op for cascade purposes.
	cascadeTripped := false
	cascadeTripReason := ""

	// Remove from running
	filtered := as.state.Running[:0]
	var title string
	for _, r := range as.state.Running {
		if r.Number == issue && r.Repo == repo {
			title = r.Title
			continue
		}
		filtered = append(filtered, r)
	}
	as.state.Running = filtered

	if success {
		as.state.Completed = append(as.state.Completed, CompletedItem{
			Repo:        repo,
			Number:      issue,
			Title:       title,
			CompletedAt: now,
		})
		// Record closure timestamp to guard against GitHub read-after-write
		// race in reconcileStateAgainstGraph (Issue #3661).
		if as.recentClosures == nil {
			as.recentClosures = make(map[string]time.Time)
		}
		key := fmt.Sprintf("%s#%d", repo, issue)
		as.recentClosures[key] = time.Now()
		// Clear all backoff/conflict counts on success.
		// (key already computed above)
		if as.perIssueFailureCount != nil {
			delete(as.perIssueFailureCount, key)
		}
		if as.retryBackoff != nil {
			delete(as.retryBackoff, key)
		}
		if as.conflictRestartCount != nil {
			delete(as.conflictRestartCount, key)
		}
		// #3020: A successful completion clears the lifetime failure count
		// for this issue too — it's no longer chronically broken.
		if as.state.LifetimeIssueFailures != nil {
			delete(as.state.LifetimeIssueFailures, key)
		}
		log.Printf("autonomous: completed %s#%d — triggering cascade re-scan + promotion", repo, issue)

		// Promote newly-unblocked downstream issues from Backlog → Ready.
		// This runs in a goroutine because it makes network calls (graph build +
		// MoveStatus) and we're holding the mutex.
		go as.promoteUnblockedToReady(repo, issue)
	} else {
		key := fmt.Sprintf("%s#%d", repo, issue)

		// LEGACY fresh-branch conflict restart (#4072 gating). The modern
		// conflict path resolves in-place via the conflict-recovery loop's
		// feature-dev rewind WITHIN the run, so a successful resolution returns
		// here with success=true (handled above) and an exhausted/failed
		// resolution returns with conflictRestart=false → the normal failure
		// path below surfaces it as a true failure (#4073 turns that into a
		// stuck-epic signal). This branch only fires for a residual fresh-branch
		// signal, which the current pr-merge skill no longer emits. When it does
		// fire, don't count toward the circuit breaker for the first
		// MaxConflictRestarts attempts — concurrent-branch collisions are
		// infrastructure, not code failures.
		if conflictRestart {
			if as.conflictRestartCount == nil {
				as.conflictRestartCount = make(map[string]int)
			}
			as.conflictRestartCount[key]++
			restartNum := as.conflictRestartCount[key]

			if restartNum < MaxConflictRestarts {
				// Re-queue with minimal backoff — the branch collision is self-healing
				// once the TypeScript layer creates a fresh branch from current main.
				as.recordFailureLocked(repo, issue, title, now,
					fmt.Sprintf("conflict restart #%d — fresh branch will be created", restartNum))
				if as.retryBackoff == nil {
					as.retryBackoff = make(map[string]time.Time)
				}
				// Short backoff: 30s so the fresh branch is ready before the next scan.
				as.retryBackoff[key] = time.Now().Add(30 * time.Second)
				log.Printf("autonomous: conflict restart #%d/%d for %s — skipping circuit breaker, retry in 30s",
					restartNum, MaxConflictRestarts, key)
				// Skip circuit-breaker increment — fall through to re-scan.
				as.persistStateLocked()
				select {
				case as.rescanCh <- struct{}{}:
				default:
				}
				return
			}

			// Exceeded max conflict restarts — treat as a genuine failure.
			log.Printf("autonomous: %s exceeded %d conflict restarts, treating as true failure",
				key, MaxConflictRestarts)
		}

		// Environmental GitHub-API quota exhaustion at pipeline-start (#3896).
		// The preflight refused to launch because the GitHub REST/GraphQL
		// bucket was below headroom. Like the Anthropic quota path this is
		// environmental and transient (bucket resets within the hour): apply a
		// GLOBAL GitHub-quota cooldown until reset so no other Ready item keeps
		// dispatching into the same exhausted bucket, revert the issue to Ready,
		// and do NOT count it toward the lifetime-failure cap. The reset time
		// comes from the live tracker reading (refreshed from X-RateLimit-* on
		// the failing run's gh calls); when unavailable the helper floors it to
		// a one-minute backoff.
		if terminalFailureKind == TerminalKindGitHubQuotaLow {
			_, _, resetAt, ok := as.gitHubQuotaSnapshot()
			if !ok {
				resetAt = time.Now().Add(time.Minute)
			}
			as.recordFailureLocked(repo, issue, title, now,
				"github-quota-low (GitHub API) — environmental, will retry after bucket reset")
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = resetAt
			as.applyGitHubQuotaCooldownLocked(resetAt, "pipeline-start preflight: bucket below headroom")
			log.Printf("autonomous: github-quota-low for %s — environmental, retry after %s (no lifetime-cap increment)",
				key, resetAt.UTC().Format(time.RFC3339))
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// GitHub unreachable at pipeline-start (#4002) — the connectivity
		// sibling of the quota branch above. The preflight could not reach
		// api.github.com (DNS down, no route), so NO auth or quota state is
		// known and nothing about the issue is at fault. Apply the SHORT
		// global cooldown (every repo is equally unreachable; dispatching
		// other Ready items would fail the same preflight), revert the issue
		// to Ready, and do NOT count it toward the lifetime-failure cap.
		// Writing the shared QuotaCooldownUntil field also arms the #3444
		// pause-decline guard, so a racing haltQueueOnSlotFailure cannot
		// pause the queue over the blip.
		if terminalFailureKind == TerminalKindGitHubNetworkOutage {
			resetAt := time.Now().Add(githubNetworkOutageCooldown)
			as.recordFailureLocked(repo, issue, title, now,
				"github-network-outage (api.github.com unreachable) — environmental, will retry after cooldown")
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = resetAt
			as.applyGitHubQuotaCooldownLocked(resetAt, "pipeline-start preflight: api.github.com unreachable (network outage)")
			log.Printf("autonomous: github-network-outage for %s — environmental, retry after %s (no lifetime-cap increment, no pause)",
				key, resetAt.UTC().Format(time.RFC3339))
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Environmental Anthropic-API failures (#3398 stream_idle_timeout,
		// #3386 rate_limit_quota_exhausted): cause is the upstream API, not
		// the issue. Re-dispatching under the same conditions would burn
		// another full run's worth of tokens and very likely re-fail the
		// same way. Apply a 1-hour per-issue backoff and — critically — a
		// GLOBAL cooldown that suspends ALL dispatches until the bucket
		// resets, so other Ready items don't continue feeding work into
		// the same exhausted quota window (Issue #3431). Exempt the
		// failure from the lifetime-failure cap and the per-issue session
		// counter so the issue is treated as cleanly retryable once API
		// conditions recover.
		if terminalFailureKind == TerminalKindStreamIdleTimeout ||
			terminalFailureKind == TerminalKindRateLimitQuotaExhausted {
			label := "stream-idle-timeout"
			if terminalFailureKind == TerminalKindRateLimitQuotaExhausted {
				label = "rate-limit-quota-exhausted"
			}
			as.recordFailureLocked(repo, issue, title, now,
				fmt.Sprintf("%s (Anthropic API) — environmental, will retry after 1h", label))
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(streamIdleTimeoutBackoff)
			// #3431: GLOBAL cooldown derived from the failure-text resetsAt
			// hint when present (preferred — runs until the actual bucket
			// reset), or the streamIdleTimeoutBackoff floor as a fallback.
			as.applyQuotaCooldownLocked(label, key, failureDetail)
			log.Printf("autonomous: %s for %s — environmental failure, retry in %v (no lifetime-cap increment)",
				label, key, streamIdleTimeoutBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			// Trigger an immediate re-scan so other unblocked items proceed
			// while this one waits out its backoff.
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			// Short-circuit: skip the rest of the failure-recording path
			// so safety rails / persist / re-scan don't double-fire below.
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Stall-kills are transient infrastructure events (agent exceeded its
		// idle or hard-cap threshold, not a code defect). Treat them like
		// stream-idle-timeout: apply a fixed backoff, do NOT increment the
		// lifetime failure cap, and do NOT count toward the per-session circuit
		// breaker. This prevents repeated infrastructure stalls from
		// permanently blocking a valid issue or halting the queue. Without
		// this branch, two stall-kills on the same issue exhaust
		// MaxLifetimeFailuresPerIssue (=2) and require manual triage every
		// time — which defeats the purpose of autonomous mode.
		if terminalFailureKind == TerminalKindStallKill {
			as.recordFailureLocked(repo, issue, title, now,
				"stall-killed (transient) — will retry after backoff")
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(stallKillBackoff)
			log.Printf("autonomous: stall-kill for %s — transient, retry in %v (no lifetime-cap increment)",
				key, stallKillBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// API "Overloaded" (Anthropic 529) is a transient capacity blip — nothing
		// is wrong in our code or the issue, and it clears within minutes. Treat
		// it like a stall-kill: short backoff, NO lifetime-cap increment, NO
		// queue pause, board→Ready so the item re-dispatches. Unlike
		// quota-exhaustion it does NOT apply a global cooldown — only this issue
		// waits out the brief backoff while the rest of the queue keeps flowing.
		// Without this branch the error falls through to subagent_crash and halts
		// the whole queue on a momentary API hiccup. Issue #3835 (WS4).
		//
		// api_connection_lost (#4002) is the transport sibling: the Anthropic
		// stream died on a socket close / DNS failure (local network blip)
		// rather than a 529. Identical recovery — the blip clears on its own
		// within seconds-to-minutes and nothing about the issue is at fault.
		if terminalFailureKind == TerminalKindApiOverloaded ||
			terminalFailureKind == TerminalKindApiConnectionLost {
			label := "api-overloaded (Anthropic 529, transient)"
			if terminalFailureKind == TerminalKindApiConnectionLost {
				label = "api-connection-lost (Anthropic transport drop, transient)"
			}
			as.recordFailureLocked(repo, issue, title, now,
				label+" — will retry after backoff")
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(apiOverloadedBackoff)
			log.Printf("autonomous: %s for %s — transient, retry in %v (no lifetime-cap increment, no pause)",
				terminalFailureKind, key, apiOverloadedBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Adapter auth pre-flight failure (#312). The pipeline-start auth gate
		// refused to launch — an adapter probe timed out after a retry
		// (transient starvation: several cold `claude auth status` probes fired
		// concurrently right after an autonomous restart and lost the CPU race,
		// though auth was fine) or the adapter CLI is definitively logged out.
		// Either way it is NOT a subagent crash. Route it like the other
		// transient infra kinds: fixed backoff, board → Ready, NO
		// LifetimeIssueFailures increment, and — critically — do NOT feed the
		// cascade breaker (this branch returns before the generic path that
		// records the cascade failure), so three burst false-negatives can't
		// trip the circuit breaker and pause the whole queue over a probe that
		// was never a real crash.
		if terminalFailureKind == TerminalKindAdapterAuthFailed {
			detail := failureDetail
			if detail == "" {
				detail = "adapter auth pre-flight failed (probe timed out or logged out)"
			}
			as.recordFailureLocked(repo, issue, title, now,
				"adapter-auth-failed (retryable infra) — will retry after backoff — "+detail)
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(stallKillBackoff)
			log.Printf("autonomous: adapter_auth_failed for %s — retryable infra, retry in %v (no lifetime-cap increment, no cascade feed, no pause)",
				key, stallKillBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Model rejected by the API with the downgrade ladder exhausted (#42).
		// Mid-run rejections fall back to a weaker tier and the run continues,
		// so reaching here means even the weakest tier was refused — an
		// environmental plan/limit condition, not a code or issue defect.
		// Model-specific usage caps reset on Anthropic's rolling windows, so:
		// quota-length backoff, NO lifetime-cap increment, board → Ready. The
		// next run re-attempts the originally-requested model (downgrades are
		// per-run state and reset).
		if terminalFailureKind == TerminalKindModelUnavailable {
			as.recordFailureLocked(repo, issue, title, now,
				"model unavailable on plan (downgrade ladder exhausted) — will retry after backoff")
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(streamIdleTimeoutBackoff)
			log.Printf("autonomous: model_unavailable for %s — environmental, retry in %v (no lifetime-cap increment, no pause)",
				key, streamIdleTimeoutBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Issue #3542: worktree_uncommitted and budget_ceiling_hit are
		// recoverable events, not code defects. worktree_uncommitted means the
		// scheduler preserved the work into a recovery commit; budget_ceiling_hit
		// means the run hit the USD ceiling (real spend, not a bug). Treat both
		// like stall-kill: fixed backoff, no LifetimeIssueFailures increment, no
		// per-session circuit-breaker count. revertFailedIssueStatus still runs
		// so the board returns to Ready and the issue can be re-dispatched.
		if terminalFailureKind == TerminalKindWorktreeUncommitted ||
			terminalFailureKind == TerminalKindBudgetCeiling {
			as.recordFailureLocked(repo, issue, title, now,
				fmt.Sprintf("%s (recoverable) — will retry after backoff", terminalFailureKind))
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(stallKillBackoff)
			log.Printf("autonomous: %s for %s — recoverable, retry in %v (no lifetime-cap increment)",
				terminalFailureKind, key, stallKillBackoff)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			return
		}

		// Issue #305: blocked-dependency deferral is a NON-FAILURE. The
		// scheduler dispatched an issue whose blockedBy dependencies are still
		// open, so the pipeline deferred before any AI stages ran — the issue
		// simply isn't ready yet, nothing is wrong with it or the factory. Do
		// NOT increment LifetimeIssueFailures, do NOT feed the cascade circuit
		// breaker, and do NOT pause autonomous. Keep the issue ELIGIBLE: revert
		// the board to Ready (like the transient branches, NOT Done) with a
		// modest backoff to avoid hot-looping if it's re-dispatched while still
		// blocked. The blocker-close requeue (refreshBlockerStates /
		// promoteUnblockedToReady) re-dispatches it once the blocker closes.
		if terminalFailureKind == TerminalKindBlockedDependency {
			detail := failureDetail
			if detail == "" {
				detail = "blocked-dependency deferral — blockedBy dependencies still open"
			}
			as.recordFailureLocked(repo, issue, title, now, detail)
			if as.retryBackoff == nil {
				as.retryBackoff = make(map[string]time.Time)
			}
			as.retryBackoff[key] = time.Now().Add(blockedDependencyBackoff)
			log.Printf("autonomous: %s#%d blocked-dependency deferral (non-failure) — board → Ready, retry in %v (no lifetime-cap increment, no pause) — %s",
				repo, issue, blockedDependencyBackoff, detail)
			as.persistStateLocked()
			go as.revertFailedIssueStatus(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			// Action Center blockedBy-deferral producer (ADR 015 §F #5): surface
			// the deferral so the operator can remove a stale edge / requeue /
			// leave, instead of the issue silently backing off (#305/#310).
			as.raiseBlockedByDeferral(repo, issue, title, detail)
			return
		}

		// Issue #3661: issue-closed is a recoverable non-failure. The issue was
		// already closed (likely by the pipeline itself in a verify-and-close run)
		// when issue-pickup started. Do NOT increment LifetimeIssueFailures, do
		// NOT count toward the per-session circuit breaker, do NOT pause
		// autonomous. Move the board to Done (not Ready) since the issue is
		// genuinely closed.
		if terminalFailureKind == TerminalKindIssueClosed {
			as.recordFailureLocked(repo, issue, title, now,
				"issue-closed (non-failure) — issue was already closed when pipeline started")
			log.Printf("autonomous: %s#%d pipeline-start-failure:issue-closed — already closed, moving board to Done (no lifetime-cap increment)",
				repo, issue)
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			go as.moveIssueToDone(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			return
		}

		// Issue #3691: pr-merge "completed but PR not merged" is an
		// externally-blocked state, not a generic failure. The TS-side
		// diagnostic in HeadlessOrchestrator.diagnosePrMergeBlocker has
		// already determined WHY the PR didn't merge (CI failures, merge
		// conflict, review required, etc.). Does NOT increment
		// LifetimeIssueFailures (a PR exists; the work product is shipped
		// and waiting). Does NOT trip the cascade circuit breaker (a stuck
		// PR is not a sign the pipeline is broken, it's a sign a specific
		// PR needs attention).
		//
		// Sideline the ISSUE, not the factory: the board moves to
		// "In review" (the truthful state — a PR exists) so the scheduler
		// stops re-dispatching it into the same external blocker, while
		// every other Ready issue keeps flowing. Pre-fix this branch paused
		// the whole scheduler AND reverted the board to Ready — the revert
		// re-dispatched the issue straight back into the same red check
		// ($4-5 burned per futile retry on bowlsheet #233/#244,
		// 2026-07-11), and the pause stopped every unrelated Ready issue.
		// Once the blocker clears, merge the open PR (or move the issue
		// back to Ready to re-run pr-merge).
		if terminalFailureKind == TerminalKindPrMergeUnmerged {
			detail := failureDetail
			if detail == "" {
				detail = "pr-merge: PR was not merged"
			}
			as.recordFailureLocked(repo, issue, title, now, detail)
			log.Printf("autonomous: %s#%d pr-merge-unmerged (externally blocked — sidelined to In review, queue continues) — %s",
				repo, issue, detail)
			if as.safetyRails != nil {
				as.safetyRails.RecordCompletion(success, 0)
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
			}
			as.persistStateLocked()
			go as.moveIssueToInReview(repo, issue)
			select {
			case as.rescanCh <- struct{}{}:
			default:
			}
			return
		}

		as.recordFailureLocked(repo, issue, title, now, "pipeline failure")

		// #3605 bullet C: feed the cascading-failure breaker. Only counts
		// genuine pipeline failures (not stall_kill / quota_exhausted /
		// worktree_uncommitted / budget_ceiling, which short-circuited
		// above) so a quiet 30 minutes of legitimate retries doesn't burn
		// down the threshold. When the breaker trips, Pause autonomous with
		// triggeredBy=safety:cascading-failures so the existing IPC status
		// emitter (autonomous.statusChanged) lights up VSCode + Discord and
		// the queue stops dispatching until explicit operator Resume.
		if as.cascadeTracker != nil {
			cascadeReason := terminalFailureKind
			if cascadeReason == "" {
				cascadeReason = "pipeline_failure"
			}
			cascadeTripped, cascadeTripReason = as.cascadeTracker.RecordFailure(
				repo, issue, cascadeReason, time.Now())
			if cascadeTripped {
				log.Printf("autonomous: %s", cascadeTripReason)
				as.state.Status = "safety_tripped"
				as.state.PauseReason = cascadeTripReason
				as.state.PauseTriggeredBy = CascadePauseReason
				as.state.PausedAt = time.Now().UTC().Format(time.RFC3339)
				// Fire status-change immediately so the IPC server emits
				// autonomous.statusChanged with the cascade pause reason —
				// the Discord notifier subscribes there.
				as.fireStatusChangeLocked()
				// Action Center cascade-pause producer (ADR 015 §F #3): the
				// two-way replacement for the one-way Discord embed — surface a
				// resume/keep-paused/triage card the operator can answer from any
				// surface.
				as.raiseCascadePause(repo, issue, cascadeTripReason)
				// Fall through to persist + revert path so the cascade pause
				// is durable and the failing issue's board status is reset.
			}
		}

		// Apply exponential backoff so a persistently-failing issue doesn't
		// immediately re-trigger and blow through the circuit breaker.
		if as.perIssueFailureCount == nil {
			as.perIssueFailureCount = make(map[string]int)
		}
		if as.retryBackoff == nil {
			as.retryBackoff = make(map[string]time.Time)
		}
		as.perIssueFailureCount[key]++
		backoff := issueBackoffDuration(as.perIssueFailureCount[key])
		as.retryBackoff[key] = time.Now().Add(backoff)

		// #3020: Increment the lifetime (cross-session) failure count. This
		// survives Resume(), so a chronically-failing issue cannot be retried
		// past the cap without explicit user triage.
		if as.state.LifetimeIssueFailures == nil {
			as.state.LifetimeIssueFailures = make(map[string]int)
		}
		as.state.LifetimeIssueFailures[key]++
		lifetime := as.state.LifetimeIssueFailures[key]

		log.Printf("autonomous: failed %s#%d (%d times this session, %d lifetime), backing off for %v",
			repo, issue, as.perIssueFailureCount[key], lifetime, backoff)

		// Revert board status from "In progress" → "Ready" so the autonomous
		// scheduler can re-pick it up after backoff expires. Without this,
		// failed issues sit at In progress forever and never re-dispatch
		// because prioritize() requires Ready/Backlog status. The revert is
		// idempotent — MoveStatus to Ready is a no-op if the issue is already
		// at Ready (e.g., terminal kill before status moved). Runs in a
		// goroutine to avoid holding as.mu during a network call.
		go as.revertFailedIssueStatus(repo, issue)
	}

	// Record completion with safety rails (tokens are tracked via AddTokensSpent)
	if as.safetyRails != nil {
		as.safetyRails.RecordCompletion(success, 0)
		safetySnap := as.safetyRails.State()
		// #3605 bullet C: preserve the cascade trip reason on the Safety
		// state so retros / dashboards can surface "what fired me" without
		// re-reading the cascade tracker. Without this overlay, the
		// safety-rails snapshot (which has its own TripReason field for
		// rate-limit / lifetime-cap trips) would clobber the cascade reason.
		if cascadeTripped && cascadeTripReason != "" && safetySnap.TripReason == "" {
			safetySnap.TripReason = cascadeTripReason
		}
		as.state.Safety = &safetySnap
	} else if cascadeTripped && cascadeTripReason != "" {
		// Safety rails missing entirely (test harness path) — surface the
		// trip reason directly so the persisted state isn't silently empty.
		as.state.Safety = &SafetyState{TripReason: cascadeTripReason}
	}

	as.persistStateLocked()

	// Trigger immediate re-scan to cascade unblocks
	select {
	case as.rescanCh <- struct{}{}:
	default:
	}
}

// revertFailedIssueStatus moves a failed issue's board status back to Ready
// so the autonomous scheduler can re-dispatch it after the per-issue backoff
// expires. Without this revert, a pipeline killed mid-flight (cost-cap,
// budget-cap, stall-kill, subagent crash) leaves the issue stuck at "In
// progress" — invisible to prioritize() because isDispatchableStatus only
// matches Ready/Backlog.
//
// Mirror of the recoverOrphanedRunning revert for the in-session case.
// Recovery handles cross-session crashes (orphaned items at startup); this
// handles single-session failures (the immediate followup to a slot fail).
//
// Idempotent + best-effort: a network failure logs a warning but does not
// retry. The next scan will re-evaluate based on whatever board status the
// issue actually has. Skipped entirely when the repo has no project config.
func (as *AutonomousScheduler) revertFailedIssueStatus(repo string, issue int) {
	owner, repoName := splitOwnerRepo(repo)
	var projectNum int
	var ownerType gh.OwnerType
	as.mu.Lock()
	for _, rc := range as.repos {
		if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
			projectNum = rc.Project
			ownerType = rc.OwnerType
			break
		}
	}
	as.mu.Unlock()
	if projectNum == 0 {
		log.Printf("autonomous: revert-status: no project config for %s — skipping #%d", repo, issue)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), boardRecoveryTimeout)
	defer cancel()
	projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
	if err := projSvc.MoveStatus(ctx, owner, repoName, issue, "Ready"); err != nil {
		log.Printf("autonomous: revert-status: failed to move %s#%d back to Ready after pipeline failure: %v",
			repo, issue, err)
		return
	}
	log.Printf("autonomous: revert-status: moved %s#%d from In progress → Ready after pipeline failure (will re-dispatch when backoff expires)",
		repo, issue)
}

// moveIssueToDone moves an issue's board status to Done. Called when
// pipeline-start-failure:issue-closed fires — the issue is already closed so
// Done is the correct terminal board state (not Ready, which would re-dispatch
// it). Modeled after revertFailedIssueStatus. Issue #3661.
func (as *AutonomousScheduler) moveIssueToDone(repo string, issue int) {
	owner, repoName := splitOwnerRepo(repo)
	var projectNum int
	var ownerType gh.OwnerType
	as.mu.Lock()
	for _, rc := range as.repos {
		if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
			projectNum = rc.Project
			ownerType = rc.OwnerType
			break
		}
	}
	as.mu.Unlock()
	if projectNum == 0 {
		log.Printf("autonomous: move-to-done: no project config for %s — skipping #%d", repo, issue)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), boardRecoveryTimeout)
	defer cancel()
	projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
	if err := projSvc.MoveStatus(ctx, owner, repoName, issue, "Done"); err != nil {
		log.Printf("autonomous: move-to-done: failed to move %s#%d to Done after issue-closed: %v",
			repo, issue, err)
		return
	}
	log.Printf("autonomous: move-to-done: moved %s#%d → Done (issue was already closed when pipeline started)",
		repo, issue)
}

// moveIssueToInReview moves an externally-blocked issue's board status to
// "In review" — the truthful state when a PR exists but cannot merge (red
// required check, review requirement, merge conflict). Keeping it OUT of
// Ready stops the scheduler from re-dispatching into the same external
// blocker, and "In review" already satisfies downstream dependency checks
// (isWorkCompleteStatus). Modeled after moveIssueToDone. Best-effort: a
// network failure logs a warning; the TS-side safety net performs the same
// move on its path.
func (as *AutonomousScheduler) moveIssueToInReview(repo string, issue int) {
	owner, repoName := splitOwnerRepo(repo)
	var projectNum int
	var ownerType gh.OwnerType
	as.mu.Lock()
	for _, rc := range as.repos {
		if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
			projectNum = rc.Project
			ownerType = rc.OwnerType
			break
		}
	}
	as.mu.Unlock()
	if projectNum == 0 {
		log.Printf("autonomous: move-to-in-review: no project config for %s — skipping #%d", repo, issue)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), boardRecoveryTimeout)
	defer cancel()
	projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
	if err := projSvc.MoveStatus(ctx, owner, repoName, issue, "In review"); err != nil {
		log.Printf("autonomous: move-to-in-review: failed to move %s#%d to In review after unmerged PR: %v",
			repo, issue, err)
		return
	}
	log.Printf("autonomous: move-to-in-review: moved %s#%d → In review (PR exists, externally blocked — no re-dispatch)",
		repo, issue)
}

// RecoverOrphanedRunning is the exported entry point for startup orphan
// recovery. It allows the Go binary serve command to reset stuck "In progress"
// items on the GitHub project board as soon as the backend starts, without
// requiring the user to click "Start Autonomous" first. Idempotent — a no-op
// when state.Running is empty, safe to call concurrently with Run().
func (as *AutonomousScheduler) RecoverOrphanedRunning(ctx context.Context) {
	as.recoverOrphanedRunning(ctx)
}

// recoverOrphanedRunning detects items left in state.Running from a previous
// crashed session and moves them back to "Ready" on the project board. This
// runs once at startup before the first scan cycle.
//
// When a session crashes (extension reload, stall kill, process termination),
// onPipelineComplete never fires — leaving issues stuck "In progress" on the
// board and consuming dispatch slots. Without recovery, the scheduler sees zero
// dispatchable items and idles forever.
//
// After recovering orphaned items, this also runs a promotion scan to pick up
// any downstream issues that became unblocked while the session was down.
func (as *AutonomousScheduler) recoverOrphanedRunning(ctx context.Context) {
	as.mu.Lock()
	orphaned := make([]RunningItem, len(as.state.Running))
	copy(orphaned, as.state.Running)
	as.mu.Unlock()

	if len(orphaned) == 0 {
		return
	}

	log.Printf("autonomous: startup recovery — found %d orphaned running item(s) from previous session", len(orphaned))

	// Decouple the board writes from the caller's context deadline so a GitHub
	// rate-limit dip pauses-and-completes (waiting out the ~75m reset on the
	// WithRateLimitWait client) instead of dying at a short caller deadline and
	// leaving items stuck "In progress" — the recurring #3976 failure. Bounded
	// by boardRecoveryTimeout so a wedged call can't hang recovery forever.
	// Mirrors revertFailedIssueStatus / moveIssueToDone / promoteUnblockedToReady;
	// WithoutCancel keeps the caller's context values while shedding its deadline.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boardRecoveryTimeout)
	defer cancel()

	recovered := 0
	for _, item := range orphaned {
		owner, repoName := splitOwnerRepo(item.Repo)
		var projectNum int
		var ownerType gh.OwnerType
		for _, rc := range as.repos {
			if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
				projectNum = rc.Project
				ownerType = rc.OwnerType
				break
			}
		}

		if projectNum == 0 {
			log.Printf("autonomous: recovery: no project config for %s — skipping #%d", item.Repo, item.Number)
			continue
		}

		projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
		if err := projSvc.MoveStatus(opCtx, owner, repoName, item.Number, "Ready"); err != nil {
			log.Printf("autonomous: recovery: failed to move %s#%d back to Ready: %v",
				item.Repo, item.Number, err)
			// Still remove from Running — the item will get re-evaluated by the
			// next scan cycle based on its actual board status.
		} else {
			log.Printf("autonomous: recovery: moved %s#%d from In Progress → Ready (orphaned from crashed session)",
				item.Repo, item.Number)
		}
		recovered++
	}

	// Clear all running entries — they're all orphaned from the previous session.
	as.mu.Lock()
	as.state.Running = nil
	as.mu.Unlock()
	as.persistState()

	if recovered > 0 {
		log.Printf("autonomous: startup recovery complete — recovered %d orphaned item(s)", recovered)
	}

	// Run promotion scan to catch downstream issues that became unblocked
	// while the session was down. Build a fresh graph and promote any Backlog
	// items whose blockers are all now closed.
	as.promoteUnblockedOnStartup(ctx)
}

// isTriagedAndUnblocked returns true if a Backlog node is fully triaged
// (Priority field set + a `type:*` label that isn't `type:epic`) and either
// has no `blockedBy` dependencies or every dependency is closed. Used by
// both startup and cascade promotion paths so the rules stay consistent.
//
// Pre-#3253 the startup path silently skipped Backlog items with no
// `blockedBy` on the rationale "no dependencies — should already be Ready
// if triaged" — but Priority + type label is itself a triage signal. The
// new rule promotes any Backlog item that meets the triage gate, which
// matches what users expect when they land 12 P1 issues with `type:bug`
// labels and no blockers and start autonomous.
func isTriagedAndUnblocked(node *depgraph.Node, graph *depgraph.Graph, adj map[string][]string) bool {
	if node == nil || !strings.EqualFold(node.State, "OPEN") {
		return false
	}
	if !strings.EqualFold(node.BoardStatus, "Backlog") {
		return false
	}
	if node.Priority == "" {
		return false
	}
	hasType := false
	for _, label := range node.Labels {
		ll := strings.ToLower(label)
		if ll == "type:epic" {
			return false
		}
		if strings.HasPrefix(ll, "type:") {
			hasType = true
		}
	}
	if !hasType {
		return false
	}
	for _, depKey := range adj[node.ID().String()] {
		depNode, exists := graph.Nodes[depKey]
		if exists && strings.EqualFold(depNode.State, "OPEN") {
			return false
		}
	}
	return true
}

// promoteUnblockedOnStartup scans all issues on the board and promotes any
// triaged Backlog item whose blockers are all closed (or that has no
// blockers) to Ready. Unlike promoteUnblockedToReady (which only checks
// downstream of a specific completed item), this checks ALL Backlog items
// — catching promotions that were missed because the session crashed
// before the callback fired and lifting fully-triaged greenfield items
// that never had `blockedBy` set in the first place (#3253).
func (as *AutonomousScheduler) promoteUnblockedOnStartup(ctx context.Context) {
	// Same rationale as recoverOrphanedRunning: the startup promotion scan builds
	// the cross-repo graph and issues board moves, all of which hit GitHub. Run
	// them on a boardRecoveryTimeout context detached from the caller's deadline
	// so a rate-limit dip waits out the reset instead of aborting mid-promotion
	// and silently leaving fully-triaged items stuck in Backlog. Issue #3976.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boardRecoveryTimeout)
	defer cancel()

	graph, err := depgraph.BuildGraph(opCtx, as.ghClient, as.repos, as.repoAliases)
	if err != nil {
		log.Printf("autonomous: startup promotion: graph build failed: %v", err)
		return
	}
	logIncompleteGraph(graph)

	adj := graph.Adjacency()
	promoted := 0

	for _, node := range graph.Nodes {
		if !isTriagedAndUnblocked(node, graph, adj) {
			continue
		}

		// A fully-unblocked node whose pickup was deferred with a
		// blocked_dependency queue pause is now re-eligible (Issue #231).
		as.resumeBlockedDependencyPause(node.Number)

		owner, repoName := splitOwnerRepo(node.Repo)
		var projectNum int
		var ownerType gh.OwnerType
		for _, rc := range as.repos {
			if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
				projectNum = rc.Project
				ownerType = rc.OwnerType
				break
			}
		}
		if projectNum == 0 {
			continue
		}

		projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
		if err := projSvc.MoveStatus(opCtx, owner, repoName, node.Number, "Ready"); err != nil {
			log.Printf("autonomous: startup promotion: failed to promote %s#%d to Ready: %v",
				node.Repo, node.Number, err)
			continue
		}
		promoted++
		log.Printf("autonomous: startup promotion: promoted %s#%d from Backlog → Ready (triaged: priority=%s)",
			node.Repo, node.Number, node.Priority)
	}

	if promoted > 0 {
		log.Printf("autonomous: startup promotion: promoted %d issue(s) to Ready", promoted)
	}
}

// promoteUnblockedToReady builds the dependency graph, finds downstream issues
// of the just-completed item, and promotes any that are now fully unblocked
// from Backlog → Ready on the project board. This ensures the autonomous
// scheduler can pick them up on the next scan cycle.
func (as *AutonomousScheduler) promoteUnblockedToReady(completedRepo string, completedIssue int) {
	ctx, cancel := context.WithTimeout(context.Background(), boardRecoveryTimeout)
	defer cancel()

	// Reuse the cached graph if it's less than 30s old — a pipeline completion
	// immediately after a cycle rebuilt the graph would otherwise double the
	// board query cost for no new information.
	as.mu.Lock()
	cached := as.graphCache
	cachedAt := as.graphCacheAt
	as.mu.Unlock()

	var graph *depgraph.Graph
	if cached != nil && time.Since(cachedAt) < 30*time.Second {
		log.Printf("autonomous: promoteUnblockedToReady: reusing fresh graph (age=%s)", time.Since(cachedAt).Round(time.Millisecond))
		graph = cached
	} else {
		var buildErr error
		if as.buildGraphFn != nil {
			graph, buildErr = as.buildGraphFn(ctx)
		} else {
			graph, buildErr = depgraph.BuildGraph(ctx, as.ghClient, as.repos, as.repoAliases)
		}
		if buildErr != nil {
			log.Printf("autonomous: promoteUnblockedToReady: graph build failed: %v", buildErr)
			return
		}
		log.Printf("autonomous: promoteUnblockedToReady: built fresh graph (%d nodes)", len(graph.Nodes))
		logIncompleteGraph(graph)
		as.mu.Lock()
		as.graphCache = graph
		as.graphCacheAt = time.Now()
		as.mu.Unlock()
	}

	// Reverse adjacency: completing "To" unblocks "From".
	// Edge semantics: From depends on To. So revAdj[To] = []From.
	completedKey := fmt.Sprintf("%s#%d", completedRepo, completedIssue)
	revAdj := graph.ReverseAdjacency()
	adj := graph.Adjacency()

	downstreamKeys := revAdj[completedKey]
	if len(downstreamKeys) == 0 {
		return
	}

	var promoted int
	for _, downKey := range downstreamKeys {
		node, exists := graph.Nodes[downKey]
		if !exists {
			continue
		}
		// Triage gate is shared with promoteUnblockedOnStartup so the cascade
		// path doesn't promote items that startup would refuse (#3253).
		if !isTriagedAndUnblocked(node, graph, adj) {
			continue
		}

		// A fully-unblocked downstream node whose pickup was deferred with a
		// blocked_dependency queue pause is now re-eligible — auto-requeue it
		// without waiting for the deps-gate promote cron (Issue #231).
		as.resumeBlockedDependencyPause(node.Number)

		// Find the repo config to get the project number for MoveStatus.
		owner, repoName := splitOwnerRepo(node.Repo)
		var projectNum int
		var ownerType gh.OwnerType
		for _, rc := range as.repos {
			if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
				projectNum = rc.Project
				ownerType = rc.OwnerType
				break
			}
		}
		if projectNum == 0 {
			log.Printf("autonomous: promoteUnblockedToReady: no project config for %s — skipping #%d", node.Repo, node.Number)
			continue
		}

		projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
		if err := projSvc.MoveStatus(ctx, owner, repoName, node.Number, "Ready"); err != nil {
			log.Printf("autonomous: promoteUnblockedToReady: failed to promote %s#%d to Ready: %v",
				node.Repo, node.Number, err)
			continue
		}
		promoted++
		log.Printf("autonomous: promoted %s#%d from Backlog → Ready (unblocked by %s#%d)",
			node.Repo, node.Number, completedRepo, completedIssue)
	}

	if promoted > 0 {
		log.Printf("autonomous: promoteUnblockedToReady: promoted %d issue(s) to Ready", promoted)
	}
}

// resumeBlockedDependencyPause resumes a queue item paused with kind=
// "blocked_dependency" (Issue #231) once the autonomous scheduler considers the
// node fully unblocked, so autonomous mode auto-requeues it without waiting for
// the deps-gate promote cron. Additive and kind-scoped: it only resumes when a
// paused blocked_dependency item exists for this issue number — baseline_ci_red
// and upstream_failure pauses are untouched. No-op when the queue scheduler is
// absent (e.g. delegated-dispatch mode).
func (as *AutonomousScheduler) resumeBlockedDependencyPause(number int) {
	if as.scheduler == nil {
		return
	}
	for _, item := range as.scheduler.ListPausedByKind("blocked_dependency") {
		if item.IssueNumber != number {
			continue
		}
		if as.scheduler.ResumeByIssueNumber(number) {
			log.Printf("autonomous: resumed blocked_dependency pause for #%d — blockers now closed", number)
		}
		return
	}
}

// isRunning checks if an item is already in the running set.
func (as *AutonomousScheduler) isRunning(repo string, number int) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	for _, r := range as.state.Running {
		if r.Repo == repo && r.Number == number {
			return true
		}
	}
	return false
}

// anyRunningFrom returns true if any currently-running item belongs to the
// given repo. Used to enforce sequential mode. Must be called without as.mu held.
func (as *AutonomousScheduler) anyRunningFrom(repo string) bool {
	return as.runningCountFrom(repo) > 0
}

// runningCountFrom returns the number of currently-running items for the
// given repo. Used to enforce per-repo concurrency caps. Must be called
// without as.mu held.
func (as *AutonomousScheduler) runningCountFrom(repo string) int {
	as.mu.Lock()
	defer as.mu.Unlock()
	count := 0
	for _, r := range as.state.Running {
		if r.Repo == repo {
			count++
		}
	}
	return count
}

// isSequentialRepo returns true if the given repo is configured for sequential
// mode (at most 1 concurrent pipeline). Equivalent to
// `maxConcurrentForRepo(repo) == 1` — when an explicit numeric cap is set, it
// takes precedence over any legacy `Sequential: true` flag.
func (as *AutonomousScheduler) isSequentialRepo(repo string) bool {
	return as.maxConcurrentForRepo(repo) == 1
}

// dispatchHeadroomFloorEnv overrides the GraphQL-remaining threshold below
// which the scheduler defers dispatching a NEW pipeline. Set to a positive
// integer to override; 0 disables the gate; invalid values fall back to the
// default. Pipelines currently in flight are unaffected — this only gates
// new dispatches so the bucket doesn't get fully exhausted.
const dispatchHeadroomFloorEnv = "NIGHTGAUGE_GITHUB_DISPATCH_FLOOR"

// defaultDispatchHeadroomFloor is the GraphQL-remaining threshold at which
// the scheduler defers a new dispatch. Each pipeline run burns ~1500-2000
// GraphQL requests across its stages (board reads, status mutations, PR
// queries, sub-issue checks). Gating at 2000 leaves enough quota for one
// pipeline to finish without exhausting the bucket; combined with the
// in-flight count, this keeps two concurrent pipelines from racing the bucket
// to zero. Per-call rateLimitFloor (default 100) is the lower backstop.
const defaultDispatchHeadroomFloor = 2000

// dispatchHeadroomFloor reads the env-override lazily so it can be retuned
// without restarting the binary. Returns 0 to disable the gate entirely.
func dispatchHeadroomFloor() int {
	v := os.Getenv(dispatchHeadroomFloorEnv)
	if v == "" {
		return defaultDispatchHeadroomFloor
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultDispatchHeadroomFloor
	}
	return n
}

// hasDispatchHeadroom reports whether GitHub's GraphQL quota has enough
// headroom to safely dispatch a new pipeline. It reads the shared rate-limit
// tracker (populated by every GitHub response's X-RateLimit-* headers), so
// the check is free — no fresh GraphQL probe needed.
//
// Returns (true, "") when safe to dispatch. Returns (false, reason) when the
// remaining quota is below the configured floor; reason is a human-readable
// string suitable for logging and the state.LastRejectionReasons map.
//
// Behavior:
//   - No ghClient / no tracker / no entry yet  → allow (no data to gate on)
//   - Floor == 0 (disabled)                    → allow
//   - Reset within 30s                         → allow (let reset clear it)
//   - Otherwise: gate iff Remaining < floor
func (as *AutonomousScheduler) hasDispatchHeadroom() (bool, string) {
	if as.ghClient == nil {
		return true, ""
	}
	tracker := as.ghClient.RateLimitTracker()
	if tracker == nil {
		return true, ""
	}
	floor := dispatchHeadroomFloor()
	if floor <= 0 {
		return true, ""
	}
	entry, _, err := tracker.Get(as.ghClient.RateLimitTrackerUser())
	if err != nil || entry == nil {
		return true, ""
	}
	// If reset is imminent, don't gate — the bucket is about to refill and
	// holding back here just adds latency.
	now := time.Now().Unix()
	if entry.ResetAt > 0 && entry.ResetAt-now < 30 {
		return true, ""
	}
	if entry.Remaining < floor {
		resetIn := time.Duration(entry.ResetAt-now) * time.Second
		if resetIn < 0 {
			resetIn = 0
		}
		reason := fmt.Sprintf(
			"github graphql headroom too low (remaining=%d < floor=%d, resets in %s)",
			entry.Remaining, floor, resetIn.Round(time.Second))
		return false, reason
	}
	return true, ""
}

// effectiveAvailableSlots returns how many additional pipelines can be
// dispatched right now, considering both the global MaxConcurrent ceiling and
// any per-repo caps. Returns 0 when all effective capacity is consumed.
//
// When every active repo has an explicit cap, the per-repo sum is the binding
// limit (e.g. MaxConcurrent=3 but only one repo with cap=1 means at most 1
// slot, not 3). When any repo has no per-repo cap, the global ceiling is the
// only constraint — that repo can absorb any remaining global capacity.
//
// Must be called without as.mu held.
func (as *AutonomousScheduler) effectiveAvailableSlots() int {
	as.mu.Lock()
	globalAvail := as.config.MaxConcurrent - len(as.state.Running)
	repos := as.repos
	as.mu.Unlock()

	if globalAvail <= 0 {
		return 0
	}

	// Reserve slots for items already sitting in the explicit queue — they are
	// about to be dispatched by fillSlots and should not be crowded out by
	// board-scan candidates in the same runCycle (#3532).
	if as.scheduler != nil {
		pending := as.scheduler.QueuePendingCount()
		if pending > 0 {
			globalAvail -= pending
			if globalAvail <= 0 {
				return 0
			}
		}
	}

	if len(repos) == 0 {
		// No repos configured — global ceiling is the only constraint.
		return globalAvail
	}

	// Sum remaining capacity across active repos. If any repo lacks a per-repo
	// cap, the global ceiling is the binding constraint (that repo is uncapped).
	totalRepoCap := 0
	for _, rc := range repos {
		cap := as.maxConcurrentForRepo(rc.FullName())
		if cap == 0 {
			return globalAvail
		}
		rem := cap - as.runningCountFrom(rc.FullName())
		if rem > 0 {
			totalRepoCap += rem
		}
	}
	if totalRepoCap < globalAvail {
		return totalRepoCap
	}
	return globalAvail
}

// maxConcurrentForRepo returns the per-repo concurrency cap. 0 means
// "no per-repo cap" (the scheduler defers to the global cap).
//
// Resolution order:
//  1. RepositoryMaxConcurrent override (when > 0) wins.
//  2. Otherwise PerRepoMax (concurrency.per_repo_max, default 1).
//
// Always returns >= 1 — every repo has a per-repo cap (default serialize).
// Accepts both short and fully-qualified ("owner/repo") names.
func (as *AutonomousScheduler) maxConcurrentForRepo(repo string) int {
	if v, ok := as.config.RepositoryMaxConcurrent[repo]; ok && v > 0 {
		return v
	}
	parts := strings.SplitN(repo, "/", 2)
	short := parts[len(parts)-1]
	if v, ok := as.config.RepositoryMaxConcurrent[short]; ok && v > 0 {
		return v
	}
	if as.config.PerRepoMax > 0 {
		return as.config.PerRepoMax
	}
	return 1 // concurrency.per_repo_max default — serialize per repo
}

// complete transitions the scheduler to a terminal state and writes a
// persistent exit event to .nightgauge/logs/autonomous-exits.jsonl
// so that crashes and shutdowns can be diagnosed after the fact.
func (as *AutonomousScheduler) complete(reason string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.state.Status = reason
	as.running = false
	as.persistStateLocked()
	as.fireStatusChangeLocked()
	log.Printf("autonomous: scheduler completed with status=%s (cycles=%d, completed=%d, failed=%d)",
		reason, as.state.CyclesRun, len(as.state.Completed), len(as.state.Failed))
	as.writeExitEvent(reason)
}

// writeExitEvent appends a JSON line to the autonomous exit log for
// post-mortem diagnosis. Called under as.mu.
func (as *AutonomousScheduler) writeExitEvent(reason string) {
	if as.workspaceRoot == "" {
		return
	}
	logDir := filepath.Join(as.workspaceRoot, ".nightgauge", "logs")
	_ = os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "autonomous-exits.jsonl")

	runningNums := make([]int, 0, len(as.state.Running))
	for _, r := range as.state.Running {
		runningNums = append(runningNums, r.Number)
	}

	entry := map[string]interface{}{
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"reason":       reason,
		"cycles":       as.state.CyclesRun,
		"completed":    len(as.state.Completed),
		"failed":       len(as.state.Failed),
		"running":      runningNums,
		"tokens_spent": as.state.TokensSpent,
		"pid":          os.Getpid(),
	}
	if as.state.Safety != nil {
		entry["safety"] = as.state.Safety
	}

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("autonomous: failed to marshal exit event: %v", err)
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("autonomous: failed to open exit log: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		log.Printf("autonomous: failed to write exit event: %v", err)
	}
}

// writeCrashExitEvent writes a crash exit record to autonomous-exits.jsonl.
// Safe to call from a panic recovery — does not acquire as.mu and reads only
// as.workspaceRoot which is set at construction and never mutated.
func (as *AutonomousScheduler) writeCrashExitEvent(panicMsg, stackTrace string) {
	if as.workspaceRoot == "" {
		return
	}
	logDir := filepath.Join(as.workspaceRoot, ".nightgauge", "logs")
	_ = os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "autonomous-exits.jsonl")

	entry := map[string]interface{}{
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
		"reason":        "crashed",
		"pid":           os.Getpid(),
		"error_message": panicMsg,
		"stack_trace":   stackTrace,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("autonomous: failed to marshal crash exit event: %v", err)
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("autonomous: failed to open exit log for crash event: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		log.Printf("autonomous: failed to write crash exit event: %v", err)
	}
}

// persistState writes the autonomous state to disk atomically.
func (as *AutonomousScheduler) persistState() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.persistStateLocked()
}

// persistStateLocked writes state to disk. Caller must hold as.mu.
func (as *AutonomousScheduler) persistStateLocked() {
	if as.workspaceRoot == "" {
		return
	}
	data, err := json.MarshalIndent(as.state, "", "  ")
	if err != nil {
		log.Printf("autonomous: failed to marshal state: %v", err)
		return
	}
	p := filepath.Join(as.workspaceRoot, autonomousStateFile)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("autonomous: failed to create dir: %v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("autonomous: failed to write state: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("autonomous: failed to rename state file: %v", err)
	}
}

// loadState reads persisted state from disk. Called on construction.
func (as *AutonomousScheduler) loadState() {
	if as.workspaceRoot == "" {
		return
	}
	p := filepath.Join(as.workspaceRoot, autonomousStateFile)
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Printf("autonomous: failed to read state: %v", err)
		return
	}
	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("autonomous: failed to parse state: %v", err)
		return
	}
	// Only restore non-terminal states as "stopped" (need explicit restart).
	// Terminal states (complete, budget_exhausted, safety_tripped) are preserved as-is.
	if loaded.Status == "running" || loaded.Status == "paused" {
		loaded.Status = "stopped"
	}
	// Preserve stale Running items on load so RecoverOrphanedRunning can
	// observe them and reset each board item to "Ready". Previously loadState
	// nil'd Running here, which ran BEFORE the serve-startup recovery
	// goroutine fired — so recovery saw an empty list and the GitHub project
	// board kept the items stuck "In progress" indefinitely. Clearing now
	// happens inside RecoverOrphanedRunning after the board writes succeed.
	if len(loaded.Running) > 0 {
		log.Printf("autonomous: carrying %d orphaned running item(s) forward for recovery", len(loaded.Running))
	}
	// Refinement orphans have no board-status side effect — safe to drop.
	if len(loaded.RefinementRunning) > 0 {
		log.Printf("autonomous: clearing %d stale refinement running items from previous session", len(loaded.RefinementRunning))
		loaded.RefinementRunning = nil
	}
	// Migrate legacy duplicate Failed entries (one-per-attempt) into the
	// deduplicated {repo, number} → AttemptCount shape. Idempotent.
	if before := len(loaded.Failed); before > 0 {
		loaded.Failed = dedupeFailedItems(loaded.Failed)
		if after := len(loaded.Failed); after < before {
			log.Printf("autonomous: migrated %d legacy failure rows into %d deduplicated entries",
				before, after)
		}
	}
	as.state = &loaded
	log.Printf("autonomous: loaded state from disk (status=%s, completed=%d, failed=%d)",
		loaded.Status, len(loaded.Completed), len(loaded.Failed))
}

// recordFailureLocked appends a failure for a specific issue, deduplicating
// against any prior FailedItem for the same `{repo, number}`. Caller MUST
// hold `as.mu`.
//
// Why this exists: the previous implementation appended a new FailedItem per
// attempt, so an issue that failed six times produced six rows in both the
// state file and the status display. Besides the display clutter, the state
// file grew unboundedly for retry-heavy issues. Dedup-on-write keeps one row
// per issue with `AttemptCount` + first/last timestamps.
func (as *AutonomousScheduler) recordFailureLocked(repo string, number int, title, failedAt, reason string) {
	for i := range as.state.Failed {
		f := &as.state.Failed[i]
		if f.Repo == repo && f.Number == number {
			f.FailedAt = failedAt
			f.Reason = reason
			if f.AttemptCount < 1 {
				// Legacy entry without count — treat as one prior attempt.
				f.AttemptCount = 1
			}
			f.AttemptCount++
			if f.FirstFailedAt == "" {
				f.FirstFailedAt = failedAt
			}
			if f.Title == "" && title != "" {
				f.Title = title
			}
			return
		}
	}
	as.state.Failed = append(as.state.Failed, FailedItem{
		Repo:          repo,
		Number:        number,
		Title:         title,
		FailedAt:      failedAt,
		Reason:        reason,
		AttemptCount:  1,
		FirstFailedAt: failedAt,
	})
}

// dedupeFailedItems collapses legacy duplicate FailedItem entries (one per
// attempt) into a single entry per `{repo, number}` with summed AttemptCount,
// earliest FirstFailedAt, and latest FailedAt/Reason. Idempotent — safe to
// run on already-deduplicated state.
func dedupeFailedItems(items []FailedItem) []FailedItem {
	if len(items) == 0 {
		return items
	}
	type key struct {
		repo   string
		number int
	}
	order := make([]key, 0, len(items))
	merged := make(map[key]*FailedItem, len(items))
	for _, f := range items {
		k := key{f.Repo, f.Number}
		attempts := f.AttemptCount
		if attempts < 1 {
			attempts = 1
		}
		existing, ok := merged[k]
		if !ok {
			copy := f
			copy.AttemptCount = attempts
			if copy.FirstFailedAt == "" {
				copy.FirstFailedAt = copy.FailedAt
			}
			merged[k] = &copy
			order = append(order, k)
			continue
		}
		existing.AttemptCount += attempts
		// FailedAt/Reason = latest by timestamp.
		if f.FailedAt > existing.FailedAt {
			existing.FailedAt = f.FailedAt
			if f.Reason != "" {
				existing.Reason = f.Reason
			}
		}
		// FirstFailedAt = earliest non-empty timestamp.
		candidateFirst := f.FirstFailedAt
		if candidateFirst == "" {
			candidateFirst = f.FailedAt
		}
		if existing.FirstFailedAt == "" || candidateFirst < existing.FirstFailedAt {
			existing.FirstFailedAt = candidateFirst
		}
		if existing.Title == "" && f.Title != "" {
			existing.Title = f.Title
		}
	}
	out := make([]FailedItem, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out
}

// reconcileStateAgainstGraph removes items from the completed/failed lists
// whose issues are still OPEN on GitHub. This handles cases where the pipeline
// recorded success/failure but failed to close the issue (e.g., cross-repo
// routing bug) or where VS Code restarted with stale state. Items whose issues
// are genuinely CLOSED remain in the lists to avoid re-processing.
func (as *AutonomousScheduler) reconcileStateAgainstGraph(g *depgraph.Graph) {
	as.mu.Lock()
	defer as.mu.Unlock()

	// Prune recentClosures entries older than 60 seconds to prevent unbounded
	// growth (Issue #3661).
	for k, ts := range as.recentClosures {
		if time.Since(ts) >= 60*time.Second {
			delete(as.recentClosures, k)
		}
	}

	// Reconcile completed list
	kept := make([]CompletedItem, 0, len(as.state.Completed))
	readmitted := 0
	for _, c := range as.state.Completed {
		key := fmt.Sprintf("%s#%d", c.Repo, c.Number)
		node, exists := g.Nodes[key]
		if exists && strings.EqualFold(node.State, "OPEN") {
			// Skip re-admission if the closure was recorded within the last
			// 60 seconds — GitHub read-after-write race (Issue #3661). The
			// issue was just closed by this process; the stale OPEN read is
			// expected and transient.
			if ts, ok := as.recentClosures[key]; ok && time.Since(ts) < 60*time.Second {
				log.Printf("autonomous: skipping re-admit for %s — closed %v ago (within 60s race guard window)", key, time.Since(ts).Round(time.Second))
				kept = append(kept, c)
				continue
			}
			// Issue is still open on GitHub — remove from completed so it
			// becomes a candidate again.
			readmitted++
			log.Printf("autonomous: re-admitting %s — marked completed but still OPEN on GitHub", key)
			continue
		}
		kept = append(kept, c)
	}
	as.state.Completed = kept

	// Reconcile failed list — re-admit items still OPEN so they can be retried,
	// and prune items whose issue is CLOSED or no longer on the project board.
	keptFailed := make([]FailedItem, 0, len(as.state.Failed))
	readmittedFailed := 0
	prunedFailed := 0
	for _, f := range as.state.Failed {
		key := fmt.Sprintf("%s#%d", f.Repo, f.Number)
		node, exists := g.Nodes[key]
		if !exists {
			// Issue no longer on project board — safe to drop.
			prunedFailed++
			log.Printf("autonomous: pruned failed %s — issue no longer on project board", key)
			continue
		}
		if strings.EqualFold(node.State, "OPEN") {
			// Still open — re-admit for potential retry.
			readmittedFailed++
			log.Printf("autonomous: re-admitting %s — marked failed but still OPEN on GitHub", key)
			continue
		}
		// Issue is CLOSED — prune the stale failure entry.
		prunedFailed++
		log.Printf("autonomous: pruned closed failure %s (attempts=%d)", key, f.AttemptCount)
	}
	as.state.Failed = keptFailed

	if readmitted > 0 || readmittedFailed > 0 || prunedFailed > 0 {
		log.Printf("autonomous: reconciled state — re-admitted %d completed + %d failed items still OPEN; pruned %d closed failures",
			readmitted, readmittedFailed, prunedFailed)
		as.persistStateLocked()
	}
}

// AddTokensSpent atomically adds tokens to the spent counter.
// Called by pipeline stage completion handlers. Also feeds the safety rails
// token tracker so budget ceiling checks use live data.
func (as *AutonomousScheduler) AddTokensSpent(tokens int64) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.state.TokensSpent += tokens
	// Mirror into safety rails for budget ceiling enforcement
	if as.safetyRails != nil {
		as.safetyRails.mu.Lock()
		as.safetyRails.state.TokensUsed = as.state.TokensSpent
		as.safetyRails.mu.Unlock()
	}
}

// SafetyRails returns the safety rails instance for external integration
// (e.g., health score updates, epic checkpoint recording).
func (as *AutonomousScheduler) SafetyRails() *SafetyRails {
	return as.safetyRails
}

// OnRefinementDispatch sets a callback for dispatching refinement to the
// TypeScript extension (IPC mode). When nil, refinement is spawned directly
// via execution.Manager (CLI mode).
func (as *AutonomousScheduler) OnRefinementDispatch(fn func(owner, repo string, issueNumber int)) {
	as.onRefinementDispatch = fn
}

// refinementIsViable reports whether the scheduler has a path to actually
// execute a refinement. True when either an IPC dispatcher is registered
// (TypeScript extension runs the skill) or the underlying execution manager
// has a CLI adapter. When both are absent — the default in VSCode IPC mode
// today — refinement would synchronously panic on adapter.BuildCommand, so
// the cycle skips entirely and the feature is effectively disabled until
// IPC refinement is wired.
func (as *AutonomousScheduler) refinementIsViable() bool {
	if as.onRefinementDispatch != nil {
		return true
	}
	if as.scheduler == nil {
		return false
	}
	execMgr := as.scheduler.ExecMgr()
	if execMgr == nil {
		return false
	}
	return execMgr.HasAdapter()
}

// runRefinementCycle scans for unrefined issues and dispatches the refinement
// skill for each candidate. Runs on its own ticker, separate from the dispatch
// loop. Refinement failures do NOT trip the dispatch circuit breaker.
func (as *AutonomousScheduler) runRefinementCycle(ctx context.Context) {
	as.mu.Lock()
	if as.state.Status != "running" {
		as.mu.Unlock()
		return
	}
	as.state.LastRefinementScanAt = time.Now().UTC().Format(time.RFC3339)
	as.mu.Unlock()

	// Skip entirely if there is no working path to execute refinement. Prevents
	// a backend-killing nil-pointer panic in refineViaCLI when VSCode IPC mode
	// hands us a Scheduler with no CLI adapter and no IPC dispatcher.
	if !as.refinementIsViable() {
		as.refinementUnavailableOnce.Do(func() {
			log.Printf("[refinement] disabled: no IPC dispatcher registered and no CLI adapter configured — skipping refinement cycles")
		})
		return
	}

	// Safety rail: refinement rate limit
	if as.safetyRails != nil {
		allowed, reason := as.safetyRails.CheckBeforeRefine()
		if !allowed {
			log.Printf("[refinement] rate limit blocked: %s", reason)
			return
		}
	}

	// Scan each repo for unrefined issues
	const refinementEmptyTTL = 5 * time.Minute
	for _, rc := range as.repos {
		owner := rc.Owner
		repo := rc.Name
		fullRepo := fmt.Sprintf("%s/%s", owner, repo)

		// Skip repos that recently returned no candidates to avoid burning
		// GitHub API quota every 60s on repos with nothing to refine.
		as.mu.Lock()
		lastEmpty := as.refinementEmptyCache[fullRepo]
		as.mu.Unlock()
		if !lastEmpty.IsZero() && time.Since(lastEmpty) < refinementEmptyTTL {
			continue
		}

		issueSvc := gh.NewIssueService(as.ghClient)
		candidates, err := issueSvc.ListIssuesExcludingLabels(ctx, owner, repo,
			[]string{gh.LabelRefined, gh.LabelEpic, "type:epic"}, 5)
		if err != nil {
			log.Printf("[refinement] %s: failed to list unrefined issues: %v", fullRepo, err)
			continue
		}

		if len(candidates) == 0 {
			as.mu.Lock()
			as.refinementEmptyCache[fullRepo] = time.Now()
			as.mu.Unlock()
			continue
		}

		// Clear empty cache when candidates are found so the next cycle
		// won't skip this repo while it still has work to do.
		as.mu.Lock()
		delete(as.refinementEmptyCache, fullRepo)
		as.mu.Unlock()

		log.Printf("[refinement] %s: found %d unrefined issue(s)", fullRepo, len(candidates))

		now := time.Now()
		dispatched := 0

		for i, candidate := range candidates {
			key := fmt.Sprintf("%s#%d", fullRepo, candidate.Number)

			// Skip if already being refined
			as.mu.Lock()
			alreadyRefining := false
			for _, r := range as.state.RefinementRunning {
				if r.Repo == fullRepo && r.Number == candidate.Number {
					alreadyRefining = true
					break
				}
			}
			as.mu.Unlock()
			if alreadyRefining {
				continue
			}

			// Skip if being dispatched through pipeline
			if as.isRunning(fullRepo, candidate.Number) {
				continue
			}

			// Skip if within per-issue cooldown
			as.mu.Lock()
			cooldownUntil, hasCooldown := as.refinementCooldown[key]
			as.mu.Unlock()
			if hasCooldown && now.Before(cooldownUntil) {
				continue
			}

			// Try to acquire the refinement semaphore (non-blocking)
			select {
			case as.refinementSem <- struct{}{}:
				// Acquired slot
			default:
				// All slots full — stop dispatching this cycle
				log.Printf("[refinement] %s: all %d refinement slots occupied", fullRepo, as.config.RefinementMaxConcurrent)
				break
			}

			if as.safetyRails != nil {
				as.safetyRails.RecordRefinementStart()
			}

			log.Printf("[refinement] Refining issue #%d (%s) — %d of %d unrefined",
				candidate.Number, candidate.Title, i+1, len(candidates))

			go as.refineIssue(ctx, owner, repo, candidate)
			dispatched++
		}

		if dispatched > 0 {
			as.persistState()
		}
	}
}

// refineIssue runs the refinement skill for a single issue. It manages state
// transitions, label updates, and board status. Releases the refinement
// semaphore when done.
func (as *AutonomousScheduler) refineIssue(ctx context.Context, owner, repo string, issue gh.UnrefinedIssue) {
	fullRepo := fmt.Sprintf("%s/%s", owner, repo)
	key := fmt.Sprintf("%s#%d", fullRepo, issue.Number)

	defer func() {
		// Release semaphore
		<-as.refinementSem
	}()

	// Recover from any panic in the refinement path so one broken skill
	// cannot kill the whole backend process. The dispatch loop keeps
	// running; this issue is marked failed and enters the normal cooldown.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		log.Printf("[refinement] panic recovered while refining %s#%d: %v\n%s",
			fullRepo, issue.Number, r, debug.Stack())
		as.mu.Lock()
		as.state.RefinementRunning = removeRefinementItem(as.state.RefinementRunning, fullRepo, issue.Number)
		as.state.RefinementFailed = append(as.state.RefinementFailed, RefinementItem{
			Repo:     fullRepo,
			Number:   issue.Number,
			Title:    issue.Title,
			FailedAt: time.Now().UTC().Format(time.RFC3339),
			Reason:   fmt.Sprintf("panic: %v", r),
		})
		as.refinementFailures[key]++
		as.mu.Unlock()
		as.persistState()
	}()
	now := time.Now().UTC().Format(time.RFC3339)

	// Mark as running + set cooldown
	as.mu.Lock()
	as.state.RefinementRunning = append(as.state.RefinementRunning, RefinementItem{
		Repo:      fullRepo,
		Number:    issue.Number,
		Title:     issue.Title,
		StartedAt: now,
	})
	as.refinementCooldown[key] = time.Now().Add(as.config.RefinementCooldown)
	as.mu.Unlock()
	as.persistState()

	// Dispatch refinement
	var refineErr error
	if as.onRefinementDispatch != nil {
		// IPC path — delegate to TypeScript extension
		as.onRefinementDispatch(owner, repo, issue.Number)
	} else {
		// CLI path — invoke skill directly via execution.Manager
		refineErr = as.refineViaCLI(ctx, owner, repo, issue.Number)
	}

	issueSvc := gh.NewIssueService(as.ghClient)

	if refineErr != nil {
		// Move from Running → Failed
		as.mu.Lock()
		as.state.RefinementRunning = removeRefinementItem(as.state.RefinementRunning, fullRepo, issue.Number)
		as.state.RefinementFailed = append(as.state.RefinementFailed, RefinementItem{
			Repo:     fullRepo,
			Number:   issue.Number,
			Title:    issue.Title,
			FailedAt: time.Now().UTC().Format(time.RFC3339),
			Reason:   refineErr.Error(),
		})
		as.refinementFailures[key]++
		as.mu.Unlock()
		as.persistState()
		log.Printf("[refinement] Failed #%d: %v", issue.Number, refineErr)
		return
	}

	// Success: add pipeline:refined label
	if err := issueSvc.MarkRefined(ctx, owner, repo, issue.Number); err != nil {
		log.Printf("[refinement] #%d: failed to add pipeline:refined label: %v", issue.Number, err)
	}

	// Determine board target status
	hasAutoProcess := false
	for _, l := range issue.Labels {
		if l == gh.LabelAutoProcess {
			hasAutoProcess = true
			break
		}
	}

	targetStatus := string(state.StatusReady)
	if !hasAutoProcess && !as.config.AutoActionable {
		targetStatus = "Backlog"
	}

	// Move status on project board (best-effort — may not be on board yet)
	for _, rc := range as.repos {
		if rc.Owner == owner && rc.Name == repo && rc.Project > 0 {
			projSvc := gh.NewProjectService(as.ghClient, owner, rc.Project, rc.OwnerType)
			if err := projSvc.MoveStatus(ctx, owner, repo, issue.Number, targetStatus); err != nil {
				log.Printf("[refinement] #%d: failed to move to %s: %v", issue.Number, targetStatus, err)
			}
			break
		}
	}

	// Remove auto-process label if present (consumed, not permanent)
	if hasAutoProcess {
		issueDetail, err := issueSvc.GetIssue(ctx, owner, repo, issue.Number)
		if err == nil {
			repoLabels, err := issueSvc.GetRepoLabels(ctx, owner, repo)
			if err == nil {
				if labelID, ok := repoLabels[gh.LabelAutoProcess]; ok {
					if err := issueSvc.RemoveLabels(ctx, issueDetail.NodeID, []string{labelID}); err != nil {
						log.Printf("[refinement] #%d: failed to remove auto-process label: %v", issue.Number, err)
					}
				}
			}
		}
	}

	// Move from Running → Completed
	as.mu.Lock()
	as.state.RefinementRunning = removeRefinementItem(as.state.RefinementRunning, fullRepo, issue.Number)
	as.state.RefinementCompleted = append(as.state.RefinementCompleted, RefinementItem{
		Repo:        fullRepo,
		Number:      issue.Number,
		Title:       issue.Title,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	})
	delete(as.refinementFailures, key)
	as.mu.Unlock()
	as.persistState()
	log.Printf("[refinement] Completed #%d → %s", issue.Number, targetStatus)
}

// refineViaCLI invokes the issue-refine skill directly via execution.Manager.
func (as *AutonomousScheduler) refineViaCLI(ctx context.Context, owner, repo string, issueNumber int) error {
	execMgr := as.scheduler.ExecMgr()
	if execMgr == nil {
		return fmt.Errorf("execution manager not available")
	}

	skillPath := filepath.Join(as.workspaceRoot, "skills", "nightgauge-issue-refine", "SKILL.md")
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		return fmt.Errorf("refinement skill not found: %s", skillPath)
	}

	fullRepo := fmt.Sprintf("%s/%s", owner, repo)
	opts := execution.StageOptions{
		Repo:        fullRepo,
		IssueNumber: issueNumber,
		Stage:       "issue-refine",
		SkillPath:   skillPath,
		Model:       "sonnet", // Refinement is a lighter workload
		Timeout:     5 * time.Minute,
		TargetRepo:  fullRepo,
	}

	result, err := execMgr.RunStage(ctx, opts)
	if err != nil {
		return fmt.Errorf("skill execution failed: %w", err)
	}
	if result != nil && result.ExitCode != 0 {
		return fmt.Errorf("skill exited with code %d", result.ExitCode)
	}

	return nil
}

// removeRefinementItem removes a refinement item by repo+number from a slice.
func removeRefinementItem(items []RefinementItem, repo string, number int) []RefinementItem {
	filtered := items[:0]
	for _, r := range items {
		if r.Repo == repo && r.Number == number {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}
