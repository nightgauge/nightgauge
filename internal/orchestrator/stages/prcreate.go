// PR-create deterministic runner — Go-native pipeline stage that bypasses the
// LLM skill when context is rich enough to render a PR title and body from a
// fixed template. Mirrors the pr-merge runner shape established in PR #3264:
// typed snapshot → pure decision rule → optional render+create → return a
// Path the scheduler uses to skip or fall through to the existing LLM path.
//
// See docs/PR_CREATE_STAGE.md for the architecture and decision matrix.
package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PRCreatePath is the outcome of a deterministic pr-create attempt.
//
//   - CreatePathCreated — the runner created the PR (or detected an
//     already-existing open PR for this branch) and wrote pr-{N}.json.
//     Scheduler skips the LLM skill.
//   - CreatePathPunt    — the runner declined to create the PR for a reason
//     that requires LLM judgment (sparse context, spike, batch, security
//     concern, push failure, …). Scheduler falls through to the existing
//     skill path.
type PRCreatePath string

const (
	CreatePathCreated PRCreatePath = "created"
	CreatePathPunt    PRCreatePath = "punt"
)

// Reason codes recorded on PRCreateResult.Reason. Free-form strings are also
// allowed — these constants name the canonical buckets so telemetry queries
// can group them.
const (
	ReasonRichContext            = "rich-context"
	ReasonAlreadyExists          = "pr-already-exists"
	ReasonMissingDevContext      = "missing-dev-context"
	ReasonMissingValidateContext = "missing-validate-context"
	ReasonMissingIssueContext    = "missing-issue-context"
	ReasonValidationNotPassed    = "validation-not-passed"
	ReasonValidateErrorCategory  = "validate-error-category"
	ReasonDeadCodeBlocked        = "dead-code-blocked"
	ReasonSecurityScanFailed     = "security-scan-failed"
	ReasonScopeDriftFailed       = "scope-drift-failed"
	ReasonChecklistUnverified    = "manual-checklist-unverified"
	ReasonSpikeIssue             = "spike-issue"
	ReasonBatchMode              = "batch-mode"
	ReasonNoChanges              = "no-changes"
	ReasonBranchIsBase           = "branch-is-base"
	ReasonPushFailed             = "push-failed"
	ReasonPushedRemoteExists     = "push-failed-remote-branch-exists"
	ReasonCreateFailed           = "create-call-failed"
	ReasonClientUnavailable      = "pr-client-unavailable"
	ReasonContextInvalidJSON     = "context-invalid-json"
)

// PRCreateResult is the outcome of a single Run invocation.
type PRCreateResult struct {
	Path       PRCreatePath
	PRNumber   int
	PRURL      string
	Title      string // rendered title (set on CreatePathCreated; useful for telemetry)
	Body       string // rendered body  (set on CreatePathCreated; useful for telemetry)
	Reason     string
	DurationMs int64
}

// PRCreateRunner is the contract the scheduler uses to invoke the deterministic
// path. The default implementation (NewDeterministicPRCreateRunner) shells out
// for git push and accepts an injected pr-client interface; tests substitute a
// fake.
type PRCreateRunner interface {
	Run(ctx context.Context, issueNumber int, repo, workdir string) (PRCreateResult, error)
}

// PRCreateSnapshot is the typed projection of issue/dev/validate/planning
// context that the decision rule and template renderers operate on. Decoupled
// from the JSON-read layer so the matrix is exhaustively unit-testable
// without touching the filesystem.
type PRCreateSnapshot struct {
	// Issue fields
	IssueNumber  int
	IssueTitle   string
	IssueType    string // feature | fix | docs | refactor | chore | spike | …
	NativeParent int    // epic parent issue number; 0 = standalone
	Branch       string
	BaseBranch   string

	// Dev fields (from dev-{N}.json)
	HasDev          bool
	FilesCreated    []string
	FilesModified   []string
	FilesDeleted    []string
	BuildStatus     string // "passed" | "failed" | "skipped" | ""
	TestsPassed     int
	TestsFailed     int
	CodeStandards   string
	SecurityReview  string
	TypeCheck       string
	DeadCodeScanDev string

	// Validate fields (from validate-{N}.json)
	HasValidate           bool
	ValidationStatus      string // "passed" | "failed" | "partial" | "skipped"
	ValidateErrorCategory string
	BuildPassed           bool
	UnitTestsPassed       bool
	IntegrationPassed     bool
	DeadCodeWarningError  bool   // true when any dead_code_warnings entry has severity == "error"
	SecurityScan          string // "passed" | "failed" | "skipped" | ""
	ScopeDrift            string // "passed" | "failed" | "skipped" | ""
	ManualChecklistOpen   bool   // true when any manual_checklist entry has verified == false

	// Planning / knowledge
	KnowledgePath    string
	KnowledgeSection string

	// Pipeline metadata
	BatchPresent bool
}

// PRCreateDecision is the output of the pure decision rule.
type PRCreateDecision struct {
	ShouldCreate bool
	Punt         bool
	Reason       string
}

// DecideCreate is the pure-function decision matrix for the pr-create stage.
//
// Decision matrix (in priority order — first match wins):
//
//	!HasDev                                    → Punt (missing-dev-context)
//	BatchPresent                               → Punt (batch-mode)
//	IssueType == "spike"                       → Punt (spike-issue)
//	Branch == BaseBranch                       → Punt (branch-is-base)
//	!HasValidate                               → Punt (missing-validate-context)
//	ValidationStatus != "passed"               → Punt (validation-not-passed)
//	ValidateErrorCategory != ""                → Punt (validate-error-category)
//	DeadCodeWarningError                       → Punt (dead-code-blocked)
//	SecurityScan == "failed"                   → Punt (security-scan-failed)
//	ScopeDrift  == "failed"                    → Punt (scope-drift-failed)
//	ManualChecklistOpen                        → Punt (manual-checklist-unverified)
//	no files changed                           → Punt (no-changes)
//	otherwise                                  → Create (rich-context)
func DecideCreate(snap PRCreateSnapshot) PRCreateDecision {
	if !snap.HasDev {
		return PRCreateDecision{Punt: true, Reason: ReasonMissingDevContext}
	}
	if snap.BatchPresent {
		return PRCreateDecision{Punt: true, Reason: ReasonBatchMode}
	}
	if strings.EqualFold(snap.IssueType, "spike") {
		return PRCreateDecision{Punt: true, Reason: ReasonSpikeIssue}
	}
	if snap.Branch == "" || snap.Branch == snap.BaseBranch {
		return PRCreateDecision{Punt: true, Reason: ReasonBranchIsBase}
	}
	if !snap.HasValidate {
		return PRCreateDecision{Punt: true, Reason: ReasonMissingValidateContext}
	}
	if snap.ValidationStatus != "passed" {
		return PRCreateDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonValidationNotPassed, snap.ValidationStatus)}
	}
	if snap.ValidateErrorCategory != "" {
		return PRCreateDecision{Punt: true, Reason: fmt.Sprintf("%s: %s", ReasonValidateErrorCategory, snap.ValidateErrorCategory)}
	}
	if snap.DeadCodeWarningError {
		return PRCreateDecision{Punt: true, Reason: ReasonDeadCodeBlocked}
	}
	if snap.SecurityScan == "failed" {
		return PRCreateDecision{Punt: true, Reason: ReasonSecurityScanFailed}
	}
	if snap.ScopeDrift == "failed" {
		return PRCreateDecision{Punt: true, Reason: ReasonScopeDriftFailed}
	}
	if snap.ManualChecklistOpen {
		return PRCreateDecision{Punt: true, Reason: ReasonChecklistUnverified}
	}
	if len(snap.FilesCreated)+len(snap.FilesModified)+len(snap.FilesDeleted) == 0 {
		return PRCreateDecision{Punt: true, Reason: ReasonNoChanges}
	}
	return PRCreateDecision{ShouldCreate: true, Reason: ReasonRichContext}
}

// typePrefixMap maps issue type → conventional-commit prefix used in PR
// titles. Mirrors the skill's existing convention so deterministic output
// matches the LLM path's title shape for the rich-context majority.
var typePrefixMap = map[string]string{
	"feature":       "feat",
	"feat":          "feat",
	"enhancement":   "feat",
	"fix":           "fix",
	"bug":           "fix",
	"bugfix":        "fix",
	"docs":          "docs",
	"documentation": "docs",
	"refactor":      "refactor",
	"chore":         "chore",
	"test":          "test",
	"perf":          "perf",
	"performance":   "perf",
	"ci":            "ci",
	"build":         "build",
	"style":         "style",
	"revert":        "revert",
	"spike":         "spike",
}

// titlePrefixRegex strips a leading conventional-commit prefix already
// embedded in the issue title (e.g. "feat: deterministic-first …" → drop
// "feat: " before re-prefixing). Only strips a known prefix; unknown prefixes
// are left intact so the title remains intelligible.
var knownPrefixes = []string{
	"feat", "fix", "docs", "refactor", "chore",
	"test", "perf", "ci", "build", "style", "revert", "spike",
	"feature", "bug", "bugfix", "documentation", "enhancement",
}

// RenderTitle renders a deterministic PR title from a snapshot. Format:
//
//	<prefix>(#N): <stripped-title>
//
// Where <prefix> is the conventional-commit prefix derived from issue type
// and <stripped-title> is the issue title with any leading
// "<knownPrefix>(<scope>)?:" segment removed. Output is byte-equal across
// repeated calls on identical input — no time, no map iteration, no env reads.
func RenderTitle(snap PRCreateSnapshot) string {
	prefix := titlePrefix(snap.IssueType)
	stripped := stripTitlePrefix(snap.IssueTitle)
	return fmt.Sprintf("%s(#%d): %s", prefix, snap.IssueNumber, stripped)
}

func titlePrefix(issueType string) string {
	if p, ok := typePrefixMap[strings.ToLower(strings.TrimSpace(issueType))]; ok {
		return p
	}
	return "chore"
}

func stripTitlePrefix(title string) string {
	t := strings.TrimSpace(title)
	colonIdx := strings.Index(t, ":")
	if colonIdx <= 0 {
		return t
	}
	head := t[:colonIdx]
	// Drop optional "(scope)" suffix from the head before matching.
	if open := strings.Index(head, "("); open > 0 {
		head = head[:open]
	}
	head = strings.ToLower(strings.TrimSpace(head))
	for _, p := range knownPrefixes {
		if head == p {
			return strings.TrimSpace(t[colonIdx+1:])
		}
	}
	return t
}

// RenderBody renders a deterministic PR body from a snapshot. Sections appear
// in fixed order:
//
//	## Summary       (always)
//	## Changes       (always; sorted file lists)
//	## Validation    (always)
//	## Knowledge     (only when KnowledgeSection is non-empty)
//	closing keywords (Closes #N; "Part of #PARENT" when NativeParent > 0)
//
// Output is byte-equal across repeated calls on identical input.
func RenderBody(snap PRCreateSnapshot) string {
	var b strings.Builder

	// Summary
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("Implements #%d: %s\n\n", snap.IssueNumber, stripTitlePrefix(snap.IssueTitle)))

	// Changes — sorted lists for determinism.
	b.WriteString("## Changes\n\n")
	created := append([]string{}, snap.FilesCreated...)
	modified := append([]string{}, snap.FilesModified...)
	deleted := append([]string{}, snap.FilesDeleted...)
	sort.Strings(created)
	sort.Strings(modified)
	sort.Strings(deleted)
	if len(created) > 0 {
		b.WriteString("Created:\n")
		for _, f := range created {
			b.WriteString(fmt.Sprintf("- %s\n", f))
		}
		b.WriteString("\n")
	}
	if len(modified) > 0 {
		b.WriteString("Modified:\n")
		for _, f := range modified {
			b.WriteString(fmt.Sprintf("- %s\n", f))
		}
		b.WriteString("\n")
	}
	if len(deleted) > 0 {
		b.WriteString("Deleted:\n")
		for _, f := range deleted {
			b.WriteString(fmt.Sprintf("- %s\n", f))
		}
		b.WriteString("\n")
	}

	// Validation
	b.WriteString("## Validation\n\n")
	b.WriteString(fmt.Sprintf("- Build: %s\n", buildLabel(snap.BuildPassed, snap.BuildStatus)))
	b.WriteString(fmt.Sprintf("- Unit tests: %s (%d passed, %d failed)\n",
		passLabel(snap.UnitTestsPassed), snap.TestsPassed, snap.TestsFailed))
	if snap.IntegrationPassed {
		b.WriteString("- Integration tests: passed\n")
	}
	if snap.SecurityScan != "" && snap.SecurityScan != "skipped" {
		b.WriteString(fmt.Sprintf("- Security scan: %s\n", snap.SecurityScan))
	}
	if snap.ScopeDrift != "" && snap.ScopeDrift != "skipped" {
		b.WriteString(fmt.Sprintf("- Scope drift: %s\n", snap.ScopeDrift))
	}
	b.WriteString("\n")

	// Knowledge — caller pre-renders the block (or leaves empty).
	if snap.KnowledgeSection != "" {
		b.WriteString(strings.TrimRight(snap.KnowledgeSection, "\n"))
		b.WriteString("\n\n")
	}

	// Closing keywords — Part of first (so GitHub renders it above Closes
	// in the PR sidebar's linked-issues list), then Closes.
	if snap.NativeParent > 0 {
		b.WriteString(fmt.Sprintf("Part of #%d\n", snap.NativeParent))
	}
	b.WriteString(fmt.Sprintf("Closes #%d\n", snap.IssueNumber))

	return b.String()
}

func buildLabel(passed bool, status string) string {
	if passed {
		return "passed"
	}
	if status == "" {
		return "skipped"
	}
	return status
}

func passLabel(passed bool) string {
	if passed {
		return "passed"
	}
	return "not run"
}

// ---------------------------------------------------------------------------
// Runner / clients
// ---------------------------------------------------------------------------

// CreatedPR is the projection of a created (or pre-existing) PR returned by
// prCreateClient implementations. Decoupled from internal/github types so the
// stages package stays free of GitHub SDK dependencies.
type CreatedPR struct {
	Number int
	URL    string
	NodeID string
}

// prCreateClient abstracts the GitHub PR-create surface. Production wires this
// to internal/github.PRService via an adapter; tests substitute a fake.
type prCreateClient interface {
	GetRepoID(ctx context.Context, owner, repo string) (string, error)
	CreatePR(ctx context.Context, repoID, title, body, head, base string) (*CreatedPR, error)
	ListOpenPRsForBranch(ctx context.Context, owner, repo, head string) ([]CreatedPR, error)
}

// gitClient abstracts the local git operations the runner needs. Production
// uses an exec-backed implementation; tests inject a fake.
type gitClient interface {
	PushBranch(ctx context.Context, workdir, branch string) error
	// RemoteBranchExists reports whether origin already has the named branch.
	// Used to make pr-create idempotent on the branch: if feature-dev already
	// pushed it, a local push that is rejected (e.g. diverged worktree) must not
	// dead-end the stage — the work is already on the remote and the PR can be
	// opened from it.
	RemoteBranchExists(ctx context.Context, workdir, branch string) (bool, error)
}

// DeterministicPRCreateRunner is the default PRCreateRunner implementation.
// Reads issue/dev/validate context, evaluates DecideCreate, optionally pushes
// the branch and calls prClient.CreatePR, and writes pr-{N}.json. When either
// client is nil the runner punts so production cannot accidentally create
// half-configured PRs.
type DeterministicPRCreateRunner struct {
	prClient prCreateClient
	git      gitClient
	now      func() time.Time

	// Hook for tests to read context from a synthetic source. Returns the
	// snapshot AND a marker indicating whether dev-batch-{E}.json was found
	// (which forces a punt regardless of other fields).
	readContext func(workdir string, issueNumber int) (PRCreateSnapshot, error)

	// writeContext writes pr-{N}.json after a successful create. Indirected
	// so tests can capture the output without touching disk.
	writeContext func(workdir string, payload prContextPayload) error
}

// NewDeterministicPRCreateRunner builds a runner with default file-backed
// readers and writers. Both prClient and git are nil — callers MUST inject
// real implementations via WithPRCreateClient / WithGitClient before the
// runner can return CreatePathCreated. Without them, every Run punts with
// ReasonClientUnavailable so production wiring failures are loud and
// downstream LLM logic still runs.
func NewDeterministicPRCreateRunner() *DeterministicPRCreateRunner {
	return &DeterministicPRCreateRunner{
		now:          time.Now,
		readContext:  defaultReadCreateContext,
		writeContext: defaultWritePRContext,
	}
}

// WithPRCreateClient injects the GitHub PR-create client. Returns the runner
// for fluent chaining.
func (r *DeterministicPRCreateRunner) WithPRCreateClient(c prCreateClient) *DeterministicPRCreateRunner {
	r.prClient = c
	return r
}

// WithGitClient injects the local git client. Returns the runner for fluent
// chaining.
func (r *DeterministicPRCreateRunner) WithGitClient(g gitClient) *DeterministicPRCreateRunner {
	r.git = g
	return r
}

// Run implements PRCreateRunner.
func (r *DeterministicPRCreateRunner) Run(ctx context.Context, issueNumber int, repo, workdir string) (PRCreateResult, error) {
	start := r.now()
	finish := func(res PRCreateResult, err error) (PRCreateResult, error) {
		res.DurationMs = r.now().Sub(start).Milliseconds()
		return res, err
	}

	snap, err := r.readContext(workdir, issueNumber)
	if err != nil {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: ReasonContextInvalidJSON}, nil)
	}
	snap.IssueNumber = issueNumber

	decision := DecideCreate(snap)
	if !decision.ShouldCreate {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: decision.Reason}, nil)
	}

	if r.prClient == nil || r.git == nil {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: ReasonClientUnavailable}, nil)
	}

	owner, repoName := splitOwnerRepo(repo)
	if owner == "" || repoName == "" {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: ReasonClientUnavailable}, nil)
	}

	// Re-use existing open PR for this branch if present (idempotency).
	existing, listErr := r.prClient.ListOpenPRsForBranch(ctx, owner, repoName, snap.Branch)
	if listErr == nil && len(existing) > 0 {
		title := RenderTitle(snap)
		body := RenderBody(snap)
		_ = r.writeContext(workdir, prContextPayload{
			IssueNumber:   issueNumber,
			PRNumber:      existing[0].Number,
			PRURL:         existing[0].URL,
			Title:         title,
			BaseBranch:    snap.BaseBranch,
			KnowledgePath: snap.KnowledgePath,
		})
		return finish(PRCreateResult{
			Path:     CreatePathCreated,
			PRNumber: existing[0].Number,
			PRURL:    existing[0].URL,
			Title:    title,
			Body:     body,
			Reason:   ReasonAlreadyExists,
		}, nil)
	}

	// Push the feature branch. A push can be rejected when the worktree's local
	// branch has diverged from what feature-dev already pushed (the #3804 case).
	// Rather than punt to the LLM path — which then attempts a (correctly) blocked
	// force-push and dead-ends on AskUserQuestion in headless mode (#3828) — make
	// pr-create idempotent on the branch: if origin already has it, the work is
	// on the remote and the PR can be opened from it. Only punt when the branch
	// is genuinely absent (or existence can't be determined).
	createReason := ReasonRichContext
	if pushErr := r.git.PushBranch(ctx, workdir, snap.Branch); pushErr != nil {
		exists, existsErr := r.git.RemoteBranchExists(ctx, workdir, snap.Branch)
		if existsErr != nil || !exists {
			return finish(PRCreateResult{Path: CreatePathPunt, Reason: fmt.Sprintf("%s: %s", ReasonPushFailed, truncateErr(pushErr, 200))}, nil)
		}
		// Branch already on origin — proceed to open the PR from it.
		createReason = ReasonPushedRemoteExists
	}

	repoID, idErr := r.prClient.GetRepoID(ctx, owner, repoName)
	if idErr != nil {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: fmt.Sprintf("%s: %s", ReasonCreateFailed, truncateErr(idErr, 200))}, nil)
	}

	title := RenderTitle(snap)
	body := RenderBody(snap)
	pr, createErr := r.prClient.CreatePR(ctx, repoID, title, body, snap.Branch, snap.BaseBranch)
	if createErr != nil {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: fmt.Sprintf("%s: %s", ReasonCreateFailed, truncateErr(createErr, 200))}, nil)
	}
	if pr == nil || pr.Number <= 0 {
		return finish(PRCreateResult{Path: CreatePathPunt, Reason: ReasonCreateFailed}, nil)
	}

	if writeErr := r.writeContext(workdir, prContextPayload{
		IssueNumber:   issueNumber,
		PRNumber:      pr.Number,
		PRURL:         pr.URL,
		Title:         title,
		BaseBranch:    snap.BaseBranch,
		KnowledgePath: snap.KnowledgePath,
	}); writeErr != nil {
		return finish(PRCreateResult{
			Path:     CreatePathCreated,
			PRNumber: pr.Number,
			PRURL:    pr.URL,
			Title:    title,
			Body:     body,
			Reason:   fmt.Sprintf("%s + context-write-warn: %s", createReason, truncateErr(writeErr, 200)),
		}, nil)
	}

	return finish(PRCreateResult{
		Path:     CreatePathCreated,
		PRNumber: pr.Number,
		PRURL:    pr.URL,
		Title:    title,
		Body:     body,
		Reason:   createReason,
	}, nil)
}

// splitOwnerRepo parses "owner/repo" → ("owner", "repo"). Returns ("", "")
// when the input is malformed.
func splitOwnerRepo(full string) (string, string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// ---------------------------------------------------------------------------
// Context I/O
// ---------------------------------------------------------------------------

// prContextPayload is the subset of pr-{N}.json fields the deterministic
// runner can populate without LLM authoring. Mirrors Phase 4 of the existing
// pr-create skill — fields the runner cannot compute (preflight_results,
// ci_monitoring) are left at their zero values for downstream consumers to
// treat as "not run". The schema is additive — pr-merge consumers
// (#3264) only read pr_number, so this payload remains compatible.
type prContextPayload struct {
	IssueNumber   int    `json:"issue_number"`
	PRNumber      int    `json:"pr_number"`
	PRURL         string `json:"pr_url"`
	Title         string `json:"title"`
	BaseBranch    string `json:"base_branch"`
	Status        string `json:"status"`
	SchemaVersion string `json:"schema_version"`
	// Reviewers is required (non-null array) by the SDK PRContextSchema that the
	// TS HeadlessOrchestrator validates pr-{N}.json against on its
	// deterministic-first pr-create path (#300). The Go scheduler never runs that
	// Zod validation, but the dogfood TS path does — a nil slice marshals to
	// `null` and fails `z.array(z.string())`, so it is always emitted as `[]`.
	Reviewers        []string          `json:"reviewers"`
	KnowledgePath    string            `json:"knowledge_path,omitempty"`
	PreflightResults map[string]string `json:"preflight_results"`
	CIMonitoring     map[string]any    `json:"ci_monitoring"`
	CreatedAt        string            `json:"created_at"`
}

// defaultWritePRContext writes pr-{N}.json under workdir. Indirected through
// readFile/writeFile so tests can stub.
func defaultWritePRContext(workdir string, p prContextPayload) error {
	if p.SchemaVersion == "" {
		p.SchemaVersion = "1.0"
	}
	if p.Status == "" {
		p.Status = "open"
	}
	if p.Reviewers == nil {
		p.Reviewers = []string{}
	}
	if p.PreflightResults == nil {
		p.PreflightResults = map[string]string{
			"json_validation":     "skipped",
			"yaml_validation":     "skipped",
			"version_consistency": "skipped",
			"security_scan":       "skipped",
			"coverage_check":      "skipped",
			"scope_drift_check":   "skipped",
		}
	}
	if p.CIMonitoring == nil {
		p.CIMonitoring = map[string]any{
			"monitored":             false,
			"monitor_duration_secs": 0,
			"final_status":          "pending",
			"checks_summary": map[string]int{
				"total":   0,
				"passed":  0,
				"failed":  0,
				"pending": 0,
			},
			"failures":  []any{},
			"timestamp": nil,
			"notes":     "",
		}
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	dir := filepath.Join(workdir, ".nightgauge", "pipeline")
	path := filepath.Join(dir, fmt.Sprintf("pr-%d.json", p.IssueNumber))
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pr context: %w", err)
	}
	if err := mkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir pr context dir: %w", err)
	}
	return writeFileAtomic(path, data)
}

// defaultReadCreateContext loads issue/dev/validate/planning context from
// .nightgauge/pipeline/ and projects them into a PRCreateSnapshot.
// Missing dev-{N}.json sets HasDev=false (decision rule punts). Missing
// validate-{N}.json sets HasValidate=false. Missing issue-{N}.json is
// tolerated — the snapshot keeps zero-values.
func defaultReadCreateContext(workdir string, issueNumber int) (PRCreateSnapshot, error) {
	snap := PRCreateSnapshot{}

	// dev-batch-{E}.json — when present, force batch mode regardless of other fields.
	batchPath := filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("dev-batch-%d.json", issueNumber))
	if _, err := readFile(batchPath); err == nil {
		snap.BatchPresent = true
	}

	// issue-{N}.json
	if data, err := readFile(filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("issue-%d.json", issueNumber))); err == nil {
		var raw struct {
			IssueNumber  int      `json:"issue_number"`
			Title        string   `json:"title"`
			Type         string   `json:"type"`
			NativeParent *int     `json:"native_parent"`
			Branch       string   `json:"branch"`
			BaseBranch   string   `json:"base_branch"`
			Labels       []string `json:"labels"`
		}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			return snap, fmt.Errorf("parse issue context: %w", jsonErr)
		}
		snap.IssueTitle = raw.Title
		snap.IssueType = raw.Type
		if raw.NativeParent != nil {
			snap.NativeParent = *raw.NativeParent
		}
		snap.Branch = raw.Branch
		snap.BaseBranch = raw.BaseBranch
	}

	// dev-{N}.json
	if data, err := readFile(filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("dev-%d.json", issueNumber))); err == nil {
		var raw struct {
			FilesChanged struct {
				Created  []string `json:"created"`
				Modified []string `json:"modified"`
				Deleted  []string `json:"deleted"`
			} `json:"files_changed"`
			BuildVerification struct {
				Status string `json:"status"`
			} `json:"build_verification"`
			TestsStatus struct {
				Passed int `json:"passed"`
				Failed int `json:"failed"`
			} `json:"tests_status"`
			QualityChecks struct {
				CodeStandards  string `json:"code_standards"`
				SecurityReview string `json:"security_review"`
				TypeCheck      string `json:"type_check"`
				DeadCodeScan   string `json:"dead_code_scan"`
			} `json:"quality_checks"`
			KnowledgePath string `json:"knowledge_path"`
		}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			return snap, fmt.Errorf("parse dev context: %w", jsonErr)
		}
		snap.HasDev = true
		snap.FilesCreated = raw.FilesChanged.Created
		snap.FilesModified = raw.FilesChanged.Modified
		snap.FilesDeleted = raw.FilesChanged.Deleted
		snap.BuildStatus = raw.BuildVerification.Status
		snap.TestsPassed = raw.TestsStatus.Passed
		snap.TestsFailed = raw.TestsStatus.Failed
		snap.CodeStandards = raw.QualityChecks.CodeStandards
		snap.SecurityReview = raw.QualityChecks.SecurityReview
		snap.TypeCheck = raw.QualityChecks.TypeCheck
		snap.DeadCodeScanDev = raw.QualityChecks.DeadCodeScan
		if snap.KnowledgePath == "" {
			snap.KnowledgePath = raw.KnowledgePath
		}
	}

	// validate-{N}.json
	if data, err := readFile(filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("validate-%d.json", issueNumber))); err == nil {
		var raw struct {
			ValidationStatus string `json:"validation_status"`
			ErrorCategory    string `json:"errorCategory"`
			Build            struct {
				Passed bool `json:"passed"`
			} `json:"build"`
			UnitTests struct {
				Passed bool `json:"passed"`
			} `json:"unit_tests"`
			IntegrationTests struct {
				Passed bool `json:"passed"`
			} `json:"integration_tests"`
			DeadCodeWarnings []struct {
				Severity string `json:"severity"`
			} `json:"dead_code_warnings"`
			ManualChecklist []struct {
				Item     string `json:"item"`
				Verified bool   `json:"verified"`
			} `json:"manual_checklist"`
		}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			return snap, fmt.Errorf("parse validate context: %w", jsonErr)
		}
		snap.HasValidate = true
		snap.ValidationStatus = raw.ValidationStatus
		snap.ValidateErrorCategory = raw.ErrorCategory
		snap.BuildPassed = raw.Build.Passed
		snap.UnitTestsPassed = raw.UnitTests.Passed
		snap.IntegrationPassed = raw.IntegrationTests.Passed
		for _, w := range raw.DeadCodeWarnings {
			if strings.EqualFold(w.Severity, "error") {
				snap.DeadCodeWarningError = true
				break
			}
		}
		for _, c := range raw.ManualChecklist {
			if !c.Verified {
				snap.ManualChecklistOpen = true
				break
			}
		}
	}

	// planning-{N}.json — picks up knowledge_path when not already set.
	if snap.KnowledgePath == "" {
		if data, err := readFile(filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("planning-%d.json", issueNumber))); err == nil {
			var raw struct {
				KnowledgePath string `json:"knowledge_path"`
			}
			_ = json.Unmarshal(data, &raw)
			snap.KnowledgePath = raw.KnowledgePath
		}
	}

	return snap, nil
}

// ---------------------------------------------------------------------------
// Default exec-backed git client
// ---------------------------------------------------------------------------

// execGitClient runs `git` via os/exec in a working directory.
type execGitClient struct{}

// NewExecGitClient returns a gitClient backed by os/exec.
func NewExecGitClient() gitClient {
	return &execGitClient{}
}

func (g *execGitClient) PushBranch(ctx context.Context, workdir, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branch)
	cmd.Dir = workdir
	if _, err := cmd.Output(); err != nil {
		return normalizeGhError(err) // reuses prmerge's stderr-attaching helper
	}
	return nil
}

// RemoteBranchExists reports whether origin already has the named branch.
// `git ls-remote --exit-code --heads` exits 0 when the ref exists and 2 when it
// does not; any other error is surfaced so the caller can fall back to punting.
func (g *execGitClient) RemoteBranchExists(ctx context.Context, workdir, branch string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)) != "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		// Exit 2 == ref not found (not an error condition).
		return false, nil
	}
	return false, normalizeGhError(err)
}

// ---------------------------------------------------------------------------
// File helpers (indirected for tests)
// ---------------------------------------------------------------------------

// mkdirAll / writeFileAtomic are package-level indirections so tests can stub
// without touching the filesystem. Defaults call into os/state helpers.
var (
	mkdirAll        = osMkdirAll
	writeFileAtomic = osWriteFileAtomic
)
