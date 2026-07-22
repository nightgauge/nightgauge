package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PrMergeGate is the canonical post-condition gate for the pr-merge stage.
// It is the single source of truth that replaces the bulk of the TS-side
// HeadlessOrchestrator.verifyPostMergeState. The TS shell now spawns
// `nightgauge gate verify pr-merge <N>` and parses this gate's JSON.
//
// Gate logic:
//
//  1. pipeline/pr-{N}.json exists and records a non-zero pr_number
//  2. Primary: `gh pr view` reports state == "MERGED"
//  3. Fallback (GitHub rate-limited): a merge commit referencing the PR or
//     issue exists on origin/main. This avoids false-positive gate failures
//     when GitHub's GraphQL rate limit blocks the verification call after
//     the merge already succeeded (Issue #3372).
//
// CI-check failures are intentionally NOT a hard fail here — the existing
// behaviour is to log and continue because the PR is already merged. The
// real fix lives in feature-validate's pre-merge wait-for-checks gate.
type PrMergeGate struct{}

// Name implements StageGate.
func (PrMergeGate) Name() string { return "pr-merge" }

// Verify implements StageGate.
func (PrMergeGate) Verify(ctx context.Context, issueNumber int, workspace string) GateResult {
	return timedKind("pr-merge", func() (bool, string, []string, Kind) {
		ctxPath := contextFilePath(workspace, "pr", issueNumber)
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "pr context file missing", []string{
					fmt.Sprintf("expected %s", ctxPath),
				}, KindNoOp
			}
			return false, "failed to read pr context file", []string{err.Error()}, KindFail
		}

		var prCtx struct {
			PrNumber int    `json:"pr_number"`
			PrUrl    string `json:"pr_url"`
		}
		if err := json.Unmarshal(data, &prCtx); err != nil {
			return false, "pr context is not valid JSON", []string{err.Error()}, KindFail
		}
		if prCtx.PrNumber == 0 {
			return false, "pr context missing pr_number", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindNoOp
		}

		// Pin --repo from pr_url so the check targets the right repo in a
		// multi-repo workspace (see PrCreateGate / #3885). Empty slug falls
		// back to CWD-based resolution. We additionally fetch mergeStateStatus /
		// mergeable / reviewDecision so a non-merged PR's evidence names WHY it
		// did not merge (BEHIND/DIRTY/REVIEW_REQUIRED) — the signal the
		// branch-out-of-date recovery action keys on to rebase a stale wave
		// sibling instead of abandoning it (#4071).
		ghArgs := ghPRViewArgs(prCtx.PrNumber, repoSlugFromPRURL(prCtx.PrUrl),
			"state,number,mergeStateStatus,mergeable,reviewDecision")

		// Relaxed runs (verified-trivial change, #4128) skip the retry+sleep
		// rate-limit cushion — a docs-only PR is not worth the extra round-trips.
		attempts := 3
		if Relaxed(ctx) {
			attempts = 1
		}
		var lastErr error
		var ghOut []byte
		for attempt := 1; attempt <= attempts; attempt++ {
			ghOut, lastErr = execGh(ctx, ghArgs...)
			if lastErr == nil && !ghOutputLooksRateLimited(ghOut) {
				break
			}
			if attempt < attempts {
				time.Sleep(1 * time.Second)
			}
		}

		// gh succeeded — parse the JSON response.
		if lastErr == nil && !ghOutputLooksRateLimited(ghOut) {
			var ghResp struct {
				State            string `json:"state"`
				Number           int    `json:"number"`
				MergeStateStatus string `json:"mergeStateStatus"`
				Mergeable        string `json:"mergeable"`
				ReviewDecision   string `json:"reviewDecision"`
			}
			if err := json.Unmarshal(ghOut, &ghResp); err != nil {
				return false, "gh pr view returned unparseable JSON", []string{err.Error()}, KindFail
			}
			if ghResp.State != "MERGED" {
				// pr-merge skill said success but the PR is still OPEN/CLOSED
				// — canonical skill-no-op signal. Classifier maps to
				// PipelineOutcomeType "skill-no-op". The mergeStateStatus /
				// mergeable / reviewDecision tokens are appended to the evidence
				// so the recovery registry can tell a BEHIND/DIRTY stale-sibling
				// PR (→ rebase-before-merge) apart from a plain unflipped PR
				// (#4071).
				return false, fmt.Sprintf("PR #%d is not MERGED (state=%s, mergeStateStatus=%s)",
						prCtx.PrNumber, ghResp.State, ghResp.MergeStateStatus), []string{
						fmt.Sprintf("pr=%d", prCtx.PrNumber),
						fmt.Sprintf("state=%s", ghResp.State),
						fmt.Sprintf("mergeStateStatus=%s", ghResp.MergeStateStatus),
						fmt.Sprintf("mergeable=%s", ghResp.Mergeable),
						fmt.Sprintf("reviewDecision=%s", ghResp.ReviewDecision),
					}, KindNoOp
			}
			return true, "PR is MERGED", []string{
				fmt.Sprintf("pr=%d", prCtx.PrNumber),
			}, KindOK
		}

		// gh failed or rate-limited — fall back to local git verification.
		// The merge commit's first-line subject (after squash) carries the
		// PR number as `(#NNN)`; cross-reference also catches commits whose
		// only mention is the issue number. Both forms are accepted.
		failureReason := "unknown"
		if lastErr != nil {
			failureReason = lastErr.Error()
		} else if ghOutputLooksRateLimited(ghOut) {
			failureReason = "rate-limited"
		}
		merged, evidence := localGitMergeFallback(ctx, workspace, prCtx.PrNumber, issueNumber)
		if merged {
			return true, "PR is MERGED (verified via local git after gh fallback)", append(
				[]string{
					fmt.Sprintf("pr=%d", prCtx.PrNumber),
					fmt.Sprintf("gh_failure=%s", truncate(failureReason, 200)),
				}, evidence...), KindOK
		}
		return false, "gh pr view failed after retries and local git fallback found no merge commit", []string{
			fmt.Sprintf("pr=%d", prCtx.PrNumber),
			fmt.Sprintf("gh_failure=%s", truncate(failureReason, 200)),
		}, KindFail
	})
}

// ghOutputLooksRateLimited returns true when stdout from `gh` contains the
// telltale GitHub rate-limit string. `gh` writes the human-readable error to
// stderr but in practice we exec.Cmd.Output() which only captures stdout, and
// gh on rate-limit failure does write at least the leading "GraphQL: API rate
// limit" line to stdout when invoked with --json. Be defensive about both
// paths — if either appears in the byte slice, treat as rate-limited.
func ghOutputLooksRateLimited(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "rate limit") ||
		strings.Contains(s, "secondary rate limit")
}

// execGitForGate is the indirection point for git-backed fallback in this
// gate. Tests stub it the same way execGh is stubbed in gate.go.
var execGitForGate = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// localGitMergeFallback checks the local repo for a merge commit referencing
// either the PR or the issue. Used when `gh pr view` fails (typically due to
// a GitHub rate limit) so a successful merge that we just performed isn't
// reported as a gate failure.
//
// Strategy:
//   - Try `git log origin/main --oneline -50` and grep the subject for
//     `(#PR)` or `(#ISSUE)`. Both squash merges (one commit, subject ends
//     with `(#NNN)`) and explicit merge commits will carry one of these
//     references when produced by the `gh pr merge --squash` path the
//     pipeline uses.
//
// Note: we do NOT call `git fetch` here. The local origin/main may be a few
// commits behind the actual remote, but the pr-merge stage runs immediately
// after the merge from the same machine, so the remote-tracking ref is
// already up to date for any merge this pipeline performed. Falling further
// behind would only ever produce a false-NEGATIVE (gate fails open) — never
// a false-positive — so there is no correctness risk in skipping fetch.
func localGitMergeFallback(ctx context.Context, workspace string, prNumber, issueNumber int) (bool, []string) {
	// Search the recent history for a subject containing the PR or issue
	// reference. 100 commits is more than sufficient — pr-merge runs
	// seconds after the merge.
	out, err := execGitForGate(ctx, workspace, "log", "origin/main", "--oneline", "-100")
	if err != nil {
		return false, []string{fmt.Sprintf("git log origin/main failed: %s", truncate(err.Error(), 200))}
	}
	prRef := fmt.Sprintf("(#%d)", prNumber)
	issueRef := fmt.Sprintf("#%d", issueNumber)
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		// Accept the explicit (#PR) marker (squash-merge convention) OR
		// any reference to the issue number. The latter catches the case
		// where the conventional commit subject contains the issue number
		// but the squash-merge UI didn't append the PR number.
		if strings.Contains(line, prRef) || strings.Contains(line, issueRef) {
			return true, []string{
				fmt.Sprintf("local_git_subject=%s", truncate(line, 160)),
			}
		}
	}
	return false, []string{
		fmt.Sprintf("scanned %d origin/main commits without finding %s or %s",
			countNonEmptyLines(out), prRef, issueRef),
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func countNonEmptyLines(b []byte) int {
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			n++
		}
	}
	return n
}
