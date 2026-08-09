package ipc

import "github.com/nightgauge/nightgauge/internal/diagnostics"

// RunStageParams is the payload for the "pipeline.runStage" event (Go→TS).
// The Go scheduler emits this event to ask the TypeScript SkillRunner to
// execute a pipeline stage via the Claude CLI.
//
// The scheduler's own `prompt` is deliberately NOT on this wire (#340). The
// extension composes the stage prompt itself — it renders the skill through
// `nightgauge skill render`, may substitute a platform-resolved SKILL.md body
// (SkillContent below), and prepends the Haiku behavioral preamble — so a
// second, unread prompt travelling alongside is indistinguishable from one
// that silently broke. `Model` went the other way: it is now authoritative
// and the extension executes it verbatim.
type RunStageParams struct {
	Stage       string `json:"stage"`
	IssueNumber int    `json:"issueNumber"`
	// Model is the tier this stage runs on, and it is AUTHORITATIVE (#340).
	// resolveDispatchModel has already applied the per-stage base routing (the
	// performance-mode pin AND its [floor, ceiling] envelope,
	// pipeline.stage_models and its env overrides,
	// model_routing.mode, the lightweight stage defaults), post-failure
	// escalation, sticky model-unavailable downgrades (#42), the
	// model_routing.minimum_model floor (#366), the pr-create large-diff
	// escalation, the feature-validate haiku gate and the pr-merge haiku floor
	// (#197). The TS SkillRunner passes it straight to the CLI's --model and
	// runs no resolution of its own. On this wire only codex translates the
	// band to a provider id (resolveCodexPipelineModel); gemini/copilot/
	// lm-studio launch their configured model outside Maximum mode — the
	// mode-pinned Maximum path is the sole exception (docs/PIPELINE_EXECUTION.md
	// § Who Resolves the Model). Whatever actually launched is reported back as
	// ServedModel.
	//
	// VOCABULARY: a registry tier BAND (haiku|sonnet|opus|fable) whenever the
	// registry recognizes the model — resolveDispatchModel's last step is
	// normalizeDispatchTier. A user-defined local model the registry does not
	// know passes through as itself, because it has no band. Do not reintroduce
	// a concrete id here: the extension's band-keyed lookups (`--effort`
	// support, the performance-mode pin comparison) no-op SILENTLY on one.
	Model             string   `json:"model"`
	MaxTokens         int      `json:"maxTokens,omitempty"`
	TimeoutMs         int      `json:"timeoutMs"`
	SkillContent      string   `json:"skillContent"`
	ContextFile       string   `json:"contextFile,omitempty"`
	OutputFile        string   `json:"outputFile,omitempty"`
	WorktreeDir       string   `json:"worktreeDir"`
	Repo              string   `json:"repo"`
	AllowedTools      []string `json:"allowedTools,omitempty"`
	SkillFallbackUsed bool     `json:"skillFallbackUsed,omitempty"`
	// AutonomousMode signals to TS SkillRunner that this stage is running
	// under the autonomous scheduler, enabling escalation+pause on stall
	// instead of silent kill. Issue #2656.
	AutonomousMode bool `json:"autonomousMode,omitempty"`
	// RunID is the UUID v7 run identity this stage is dispatched under (#3557,
	// ADR-017 step 0b). REQUIRED on the wire — no omitempty: the scheduler
	// refuses to emit this event without one, so an absent `runId` key would
	// mean the emitter broke, and the consumer must be able to tell that from a
	// run that legitimately carries an id. Everything the stage reports (phase
	// transitions, live progress, its result) is booked under this value.
	RunID string `json:"runId"`
}

// StageResultParams is the payload for the "pipeline.stageResult" request (TS→Go).
// After a SkillRunner completes a stage, TypeScript sends this back to Go
// so the scheduler can decide what to do next (continue, retry, escalate, abort).
type StageResultParams struct {
	Stage           string  `json:"stage"`
	IssueNumber     int     `json:"issueNumber"`
	Success         bool    `json:"success"`
	ExitCode        int     `json:"exitCode"`
	InputTokens     int     `json:"inputTokens"`
	OutputTokens    int     `json:"outputTokens"`
	CacheReadTokens int     `json:"cacheReadTokens,omitempty"`
	CostUsd         float64 `json:"costUsd,omitempty"` // Actual cost from Claude CLI (total_cost_usd)
	FeedbackFile    string  `json:"feedbackFile,omitempty"`
	// Error classification from SkillRunner local detection or Go taxonomy (Issue #2573).
	// Values: "rate_limit", "auth", "network", "token_limit", "unknown", or empty.
	ErrorCategory string `json:"errorCategory,omitempty"`
	// For rate limit errors, exact wait duration in milliseconds until the limit resets.
	RetryAfterMs int `json:"retryAfterMs,omitempty"`
	// RunID is the UUID v7 run ID from runstate, carried back for correlation (#3557).
	RunID string `json:"runId,omitempty"`
	// ErrorText is the human-readable failure reason from the executor.
	// Required by the Go ClassifyTerminalKind heuristic to distinguish
	// stall_kill / budget_exceeded / subagent_crash so the V3 RunRecord
	// in the daily JSONL carries the correct terminal_failure_kind.
	// PipelineBridge synthesizes this from skillRunner result flags
	// (`[stall-killed]`, `[cost-cap-exceeded]`, error.message). Empty when
	// success=true. (Issue #3207)
	ErrorText string `json:"errorText,omitempty"`
	// LastOutputLines is the trailing stderr/stdout snippet captured at
	// terminal failure (≤200 lines, ≤200KB). Carried into the V3 record's
	// per-stage `last_output_lines` field so retros / dashboards have
	// evidence of what the agent was doing when it died. (Issue #3207)
	LastOutputLines string `json:"lastOutputLines,omitempty"`

	// ── #3605 stage-exit diagnostic record fields ─────────────────────
	// The TS SkillRunner is the only layer with first-hand knowledge of
	// the subprocess signal/source, the live stderr tail, the last Bash
	// tool_use, and the stop-hook stream notification. These are forwarded
	// verbatim through pipeline.stageResult so Go's scheduler can persist
	// them in .nightgauge/pipeline/exit-records/<day>.jsonl alongside
	// the data the Go side already knows (rate-limit reading, concurrent
	// sibling pipelines). All fields are optional — absent fields are not
	// written to the record so healthy runs stay terse.
	//
	// Forward-compatibility: TS may omit any of these and Go will still
	// write a valid (terser) exit record. Once the TS SkillRunner is
	// updated to populate them, the daily JSONL gains richer fields with
	// no Go-side change required. See docs/STAGE_EXIT_DIAGNOSTIC.md.

	// SessionID is the claude CLI conversation id, when one was captured
	// before exit. Empty when the subprocess never produced a `result`
	// envelope. (#3605)
	SessionID string `json:"sessionId,omitempty"`
	// Signal is the POSIX signal name (SIGTERM / SIGKILL / ...) the TS
	// SkillRunner delivered to the subprocess. Empty when the process
	// exited naturally. (#3605)
	Signal string `json:"signal,omitempty"`
	// SignalSource names the in-binary code path that delivered Signal:
	// "stall-kill" | "hard-cap" | "quota-fast-fail" | "processTree-reaper" |
	// "external" | "" (no signal). (#3605)
	SignalSource string `json:"signalSource,omitempty"`
	// KillCeiling is the stable name of the LIMIT that terminated the stage
	// and KillCeilingValue is its resolved value plus derivation (#161).
	// SignalSource names only the delivering closure; several limits share
	// each label, so it cannot identify which one fired.
	KillCeiling      string `json:"killCeiling,omitempty"`
	KillCeilingValue string `json:"killCeilingValue,omitempty"`
	// ElapsedMs is total wall time from stage start to exit (ms).
	// Optional — zero is "unknown" (Go fills its own elapsed when zero). (#3605)
	ElapsedMs int64 `json:"elapsedMs,omitempty"`
	// IdleMsAtExit is milliseconds since the last subprocess output
	// chunk at the moment of exit. Distinguishes wedged-then-killed
	// (large) from killed-mid-activity (small). (#3605)
	IdleMsAtExit int64 `json:"idleMsAtExit,omitempty"`
	// CacheCreationTokens is the cache-creation token count for the
	// stage. Mirrors the existing CacheReadTokens shape so the daily
	// exit-record carries a complete usage snapshot. (#3605)
	CacheCreationTokens int `json:"cacheCreationTokens,omitempty"`
	// LastBashCommand is the most recent `Bash` tool_use input, truncated
	// to 500 chars by the TS side before forwarding. (#3605)
	LastBashCommand string `json:"lastBashCommand,omitempty"`
	// LastBashExit is the exit code of the matching Bash tool_result.
	// Pointer-shaped so a 0 (success) is distinguishable from "never
	// observed". JSON receivers should test for null/absent. (#3605)
	LastBashExit *int `json:"lastBashExit,omitempty"`
	// RecentBash is the tail of the stage's Bash history (oldest first, at
	// most 10 entries), each carrying its own exit code. Superset of
	// LastBashCommand/LastBashExit, which keep their exact meaning — the last
	// element is the same command. Absent when the stage ran no Bash. (#156)
	RecentBash []diagnostics.RecentBashEntry `json:"recentBash,omitempty"`
	// StopHookErrored is true when the stream included a
	// `notification.key == "stop-hook-error"` event before exit. (#3605)
	StopHookErrored bool `json:"stopHookErrored,omitempty"`
	// StderrTail is the last 4 KB of stderr from the SkillRunner ring
	// buffer. Persisted verbatim to the exit-record's `stderr_tail`. (#3605)
	StderrTail string `json:"stderrTail,omitempty"`

	// ToolCalls is the stage's bounded all-tools call log (every tool_use/
	// tool_result pair observed by the TS SkillRunner, capped at 200
	// entries), forwarded verbatim so the Go scheduler can persist it onto
	// the authoritative V2RunRecord. Same "TS has first-hand knowledge"
	// category of field as LastBashCommand/RecentBash above, generalized
	// from Bash-only to all tools. (Issue #144)
	ToolCalls []diagnostics.ToolCallRecord `json:"toolCalls,omitempty"`

	// ── #3666 follow-up: budget-kill + shipped-partially via IPC ────────
	// Pre-#3666 the budget-kill signal lived only in a budget-overrun-{N}.json
	// file on disk. That contract assumes Go and TS agree on the file's
	// location, which breaks for multi-repo workspaces (TS writes to the
	// per-issue worktree, Go reads from the workspace root). These two
	// fields move the signal into IPC where it belongs — Go reads what TS
	// observed without disk-path coordination.
	//
	// BudgetExceeded is true when the BudgetEnforcer killed this stage for
	// cost-cap overrun. Set independently of Success (Success=false +
	// BudgetExceeded=true is the budget-kill path; Success=false with
	// BudgetExceeded=false is a generic failure).
	BudgetExceeded bool `json:"budgetExceeded,omitempty"`
	// ShippedPartially is true when BudgetExceeded fired but the stage's
	// work product actually shipped (e.g. pr-create killed AFTER opening
	// the PR). The Go scheduler treats this like budget_ceiling_hit — no
	// LifetimeIssueFailures increment, no cascade-breaker contribution,
	// advance to the next stage rather than retry the same one.
	ShippedPartially bool `json:"shippedPartially,omitempty"`
	// ShippedPRNumber identifies the PR the killed stage produced. Zero
	// when ShippedPartially is false. Surfaced in log lines so the
	// operator can verify the reclassification was justified.
	ShippedPRNumber int `json:"shippedPRNumber,omitempty"`

	// ── #91 / #340 served-model attribution ───────────────────────────
	// ServedModel is the model that ACTUALLY served the stage: the CLI
	// stream's last observed message.model when it reported one, otherwise the
	// concrete model the adapter PROCESS was spawned with — read out of the
	// adapter's own env after model preflight, not from the extension's
	// pre-spawn decision. Omitted only when it is byte-identical to
	// RunStageParams.Model, i.e. when there is nothing to add.
	//
	// VOCABULARY: unlike RunStageParams.Model this is a CONCRETE id, and that
	// asymmetry is deliberate. Three things make it one. The claude CLI
	// silently retries safety-refused turns on a fallback model
	// (model_refusal_fallback) and still exits 0 (#91). A non-Claude adapter
	// translates the tier band into a provider id — codex "opus" →
	// "gpt-5.6-sol", or its own configured default when no band maps. And a
	// performance-mode / supercharge override replaces that translation
	// outright. All three are the model that ran, and therefore the one
	// cost/telemetry/history must name.
	//
	// Do NOT read the omit-when-equal rule as a divergence flag: for every
	// non-Claude adapter the launched id and the requested band are different
	// strings by construction, so "present" means "here is what ran", not
	// "something went wrong". The band question — did the run serve the tier
	// the router predicted — is answered separately, by OutcomeActualBand
	// (internal/orchestrator/outcome_semantics.go), which inverts the adapter
	// mapping instead of collapsing a multi-band id onto its strongest band.
	// See docs/OUTCOME_RECORDING.md and
	// docs/spikes/fable-5-behavior-porting.md §8.3.
	ServedModel string `json:"servedModel,omitempty"`
	// RefusalFallback* echo the CLI's system/model_refusal_fallback event
	// when one was observed. Attribution + notification only — never used
	// to retry or downgrade.
	RefusalFallbackFrom     string `json:"refusalFallbackFrom,omitempty"`
	RefusalFallbackTo       string `json:"refusalFallbackTo,omitempty"`
	RefusalFallbackCategory string `json:"refusalFallbackCategory,omitempty"`
}

// AbortParams is the payload for the "pipeline.abort" event (Go→TS).
// Sent when Go decides to terminate the active stage (budget exceeded, etc.).
type AbortParams struct {
	IssueNumber int    `json:"issueNumber"`
	Reason      string `json:"reason"`
}

// LicenseCheckRequest is the payload for "pipeline.validateLicense" (Go→TS).
// The Go scheduler emits this before the stage loop to validate the user's
// license via the TypeScript PlatformApiClient.
type LicenseCheckRequest struct {
	IssueNumber int `json:"issueNumber"`
}

// LicenseCheckResult is the payload for "pipeline.licenseResult" (TS→Go).
// TypeScript validates the license and sends the result back to Go.
type LicenseCheckResult struct {
	IssueNumber int    `json:"issueNumber"`
	Allowed     bool   `json:"allowed"`
	Tier        string `json:"tier"`
	Reason      string `json:"reason,omitempty"`
	ActionURL   string `json:"actionUrl,omitempty"`
	CacheUntil  string `json:"cacheUntil,omitempty"` // ISO 8601 — re-validate when now > this
	// Status is one of platform.LicenseStatusActive/Expired/Revoked/Suspended,
	// or "" when unknown (e.g. offline degradation with no prior confirmed
	// status). Lets Go distinguish a CONFIRMED revoked/suspended license
	// (fail closed, block execution) from a generically unavailable one
	// (may still degrade gracefully). See internal/ipc/license_checker.go.
	// Issue #4156.
	Status string `json:"status,omitempty"`
}

// NOTE: RecordStageExitParams + RecordStageExitResult live in protocol.go so
// the IPC codegen picks them up — the codegen only scans protocol.go for
// type definitions referenced by `//ipc:method` annotations.
