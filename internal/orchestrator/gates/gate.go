// Package gates implements the stage post-condition verification framework
// (Issue #3266). Each pipeline stage publishes an "expected post-state" via a
// concrete StageGate; the orchestrator runs the matching gate immediately
// after the stage's skill reports success and treats a failed gate as a
// stage failure (mapped onto the existing retry/backtrack engine).
//
// Gates live in `internal/` and MUST remain deterministic — no LLM calls,
// no network beyond the `gh` queries the prior post-state logic already
// performed. See `.claude/rules/scripts.md` for the determinism rule.
//
// Naming-collision note: this package's GateResult is distinct from the
// existing state.GateResult (quality-gate build/lint/test record). The two
// types coexist because their shape and semantics differ:
//
//   - state.GateResult   : Result string ("pass" | "catch")
//   - gates.GateResult   : Passed bool + Reason + Evidence
//
// Persisted on the run record as state.StageGateResult under
// V2StageDetail.GateResults.
package gates

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Kind discriminates the *shape* of a gate outcome (Issue #3267) so callers
// can dispatch on it deterministically without regex-matching Reason strings.
//
//   - KindOK    — gate passed; post-condition satisfied.
//   - KindNoOp  — gate failed because the skill exited 0 but produced no
//     state change (missing context file, branch not created,
//     PR still OPEN after pr-merge, etc.). The classifier maps
//     this to the `skill-no-op` PipelineOutcomeType.
//   - KindFail  — gate failed because of a real error (malformed JSON,
//     unparseable gh output, IO error). Distinct from KindNoOp
//     because the work may have partially happened.
//
// Default for a passed result is KindOK; default for a failed result is
// gate-specific. New gates SHOULD set Kind explicitly; gates that don't
// (legacy paths) collapse to KindOK on pass and KindFail on fail at the
// classifier level.
type Kind string

const (
	KindOK   Kind = "ok"
	KindNoOp Kind = "no_op"
	KindFail Kind = "fail"
)

// TerminalKindValidationError mirrors
// orchestrator.TerminalKindValidationError (Issue #9). Duplicated here
// (rather than imported) because `orchestrator` imports this `gates`
// package, so the reverse import would create a cycle. The two constants
// MUST stay in sync — see ResolveTerminalKind's doc comment in
// failure_handler.go.
const TerminalKindValidationError = "validation_error"

// TerminalKindValidationFailed mirrors orchestrator.TerminalKindValidationFailed
// (Issue #9) for the same import-cycle reason as TerminalKindValidationError
// above. Set when feature-validate's own quality-gate results (not JSON
// parsing) fail — a distinct case from TerminalKindValidationError.
const TerminalKindValidationFailed = "validation_failed"

// GateResult is the in-process value returned by a StageGate.Verify call.
// The scheduler copies this into a state.StageGateResult before persisting.
type GateResult struct {
	GateName   string
	Passed     bool
	Reason     string
	Evidence   []string
	DurationMs int64
	Timestamp  string
	// Kind discriminates passed vs no-op vs hard-fail (Issue #3267).
	// See the Kind type doc for the full state machine.
	Kind Kind
	// TerminalKind optionally carries a structured TerminalKind* constant
	// (see failure_handler.go) for KindFail results whose failure shape maps
	// cleanly onto an existing constant (e.g. JSON parse failure ->
	// TerminalKindValidationError). Empty means "no gate-specific terminal
	// kind" — the caller falls back to prose classification via
	// ClassifyTerminalKind/ResolveTerminalKind (Issue #9). Always empty on
	// KindOK/KindNoOp results, which already classify structurally via Kind
	// alone.
	TerminalKind string
}

// ToStageGateResult copies the in-process GateResult into the persisted
// state.StageGateResult shape.
func (gr GateResult) ToStageGateResult() state.StageGateResult {
	return state.StageGateResult{
		GateName:     gr.GateName,
		Passed:       gr.Passed,
		Reason:       gr.Reason,
		Evidence:     append([]string(nil), gr.Evidence...),
		DurationMs:   gr.DurationMs,
		Timestamp:    gr.Timestamp,
		Kind:         string(gr.Kind),
		TerminalKind: gr.TerminalKind,
	}
}

// StageGate is the post-condition contract every pipeline stage opts into.
// Verify is deterministic: the same workspace + issue number must produce
// the same GateResult on repeated calls (modulo external state like the
// PR's GitHub-side merge status).
type StageGate interface {
	Name() string
	Verify(ctx context.Context, issueNumber int, workspace string) GateResult
}

// NoOp is the gate used for stages that have no post-condition or that have
// not yet adopted the framework. Always returns Passed=true.
type NoOp struct{ GateName string }

// Name implements StageGate.
func (n NoOp) Name() string {
	if n.GateName == "" {
		return "noop"
	}
	return n.GateName
}

// Verify implements StageGate. Always passes.
func (n NoOp) Verify(_ context.Context, _ int, _ string) GateResult {
	return GateResult{
		GateName:  n.Name(),
		Passed:    true,
		Reason:    "no post-condition registered",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Kind:      KindOK,
	}
}

// execGh is the indirection point for `gh`-backed gates so tests can
// stub GitHub API calls without spinning up a real CLI. Tests assign
// a replacement that returns canned stdout/stderr.
//
// Default implementation runs the real `gh` binary.
var execGh = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}

// ghPRViewArgs builds the `gh pr view` argument list, pinning `--repo` when a
// slug is known. Pinning the repo is REQUIRED in a multi-repo workspace: the
// scheduler's CWD is the umbrella repo (e.g. its origin is acmeapp-infra),
// so a bare `gh pr view <N>` resolves the number against the wrong repository
// and fails ("Could not resolve to a PullRequest"), producing a false-negative
// gate even when the PR was created correctly in a sibling repo (#3885). An
// empty slug preserves the legacy CWD-based resolution for single-repo setups.
func ghPRViewArgs(prNumber int, repoSlug, fields string) []string {
	if fields == "" {
		fields = "state,number"
	}
	args := []string{"pr", "view", strconv.Itoa(prNumber), "--json", fields}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	return args
}

// repoSlugFromPRURL extracts "owner/repo" from a GitHub PR URL such as
// https://github.com/nightgauge/acmeapp-platform/pull/67. Returns "" for an
// empty or non-github.com URL so callers fall back to gh's default resolution
// (e.g. GitLab PRs, which the gh-based gate does not handle anyway).
func repoSlugFromPRURL(prURL string) string {
	const marker = "github.com/"
	i := strings.Index(prURL, marker)
	if i < 0 {
		return ""
	}
	parts := strings.Split(prURL[i+len(marker):], "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// nowUTC returns the current time formatted as ISO 8601 UTC. Indirection
// keeps tests deterministic without dragging a clock parameter through
// every Verify signature.
var nowUTC = func() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// timed wraps a Verify body so per-gate implementations don't each
// compute DurationMs/Timestamp by hand. Callers pass a closure that
// returns Passed/Reason/Evidence; timed fills in the rest. Kind is
// derived from Passed: KindOK on pass, KindFail on fail. Gates that
// need to distinguish KindNoOp from KindFail use timedKind instead.
func timed(name string, fn func() (bool, string, []string)) GateResult {
	return timedKind(name, func() (bool, string, []string, Kind) {
		passed, reason, evidence := fn()
		if passed {
			return true, reason, evidence, KindOK
		}
		return false, reason, evidence, KindFail
	})
}

// timedKind is the Kind-aware variant of timed (Issue #3267). Gates that
// distinguish "skill said success but state is unchanged" (KindNoOp) from
// "real error" (KindFail) call this directly. The classifier's
// `skill-no-op` outcome is driven entirely by KindNoOp results.
func timedKind(name string, fn func() (bool, string, []string, Kind)) GateResult {
	return timedKindTerminal(name, func() (bool, string, []string, Kind, string) {
		passed, reason, evidence, kind := fn()
		return passed, reason, evidence, kind, ""
	})
}

// timedKindTerminal is the TerminalKind-aware variant of timedKind (Issue
// #9). Gates that can map a failure onto an existing TerminalKind* constant
// (see failure_handler.go) call this directly, passing the constant as the
// fifth return value; "" leaves TerminalKind unset so callers fall back to
// prose classification.
func timedKindTerminal(name string, fn func() (bool, string, []string, Kind, string)) GateResult {
	start := time.Now()
	passed, reason, evidence, kind, terminalKind := fn()
	return GateResult{
		GateName:     name,
		Passed:       passed,
		Reason:       reason,
		Evidence:     evidence,
		DurationMs:   time.Since(start).Milliseconds(),
		Timestamp:    nowUTC(),
		Kind:         kind,
		TerminalKind: terminalKind,
	}
}
