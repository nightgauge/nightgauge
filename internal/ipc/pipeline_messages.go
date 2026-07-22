package ipc

// RunStageParams is the payload for the "pipeline.runStage" event (Go→TS).
// The Go scheduler emits this event to ask the TypeScript SkillRunner to
// execute a pipeline stage via the Claude CLI.
type RunStageParams struct {
	Stage             string   `json:"stage"`
	IssueNumber       int      `json:"issueNumber"`
	Model             string   `json:"model"`
	MaxTokens         int      `json:"maxTokens,omitempty"`
	TimeoutMs         int      `json:"timeoutMs"`
	SkillContent      string   `json:"skillContent"`
	ContextFile       string   `json:"contextFile,omitempty"`
	OutputFile        string   `json:"outputFile,omitempty"`
	WorktreeDir       string   `json:"worktreeDir"`
	Repo              string   `json:"repo"`
	AllowedTools      []string `json:"allowedTools,omitempty"`
	Prompt            string   `json:"prompt,omitempty"`
	SkillFallbackUsed bool     `json:"skillFallbackUsed,omitempty"`
	// AutonomousMode signals to TS SkillRunner that this stage is running
	// under the autonomous scheduler, enabling escalation+pause on stall
	// instead of silent kill. Issue #2656.
	AutonomousMode bool `json:"autonomousMode,omitempty"`
	// RunID is the UUID v7 run ID threaded from runstate for correlation (#3557).
	RunID string `json:"runId,omitempty"`
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
	// StopHookErrored is true when the stream included a
	// `notification.key == "stop-hook-error"` event before exit. (#3605)
	StopHookErrored bool `json:"stopHookErrored,omitempty"`
	// StderrTail is the last 4 KB of stderr from the SkillRunner ring
	// buffer. Persisted verbatim to the exit-record's `stderr_tail`. (#3605)
	StderrTail string `json:"stderrTail,omitempty"`

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

	// ── #91 served-model attribution ──────────────────────────────────
	// ServedModel is the model that actually served the stage per the CLI
	// stream (last observed message.model / refusal fallback). Empty when
	// the stream carried no model info. The claude CLI silently retries
	// safety-refused turns on a fallback model (model_refusal_fallback)
	// and still exits 0, so Go must attribute cost/telemetry/history to
	// what TS observed serving, not to the model it requested.
	// See docs/spikes/fable-5-behavior-porting.md §8.3.
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
