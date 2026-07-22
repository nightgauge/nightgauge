package gates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PrCreateGate verifies the post-conditions of pr-create:
//
//  1. pipeline/pr-{N}.json exists and parses
//  2. The context records a non-zero pr_number
//  3. The PR is confirmed OPEN with the recorded number
//
// Verification prefers the GitHub REST API (`gh api repos/{slug}/pulls/{N}`),
// which draws on the *core* rate-limit bucket — deliberately NOT `gh pr view`,
// which routes through GraphQL. In the #99 incident the GraphQL bucket was
// exhausted (0/5000) while the REST core bucket was healthy (4983/5000); the
// old `gh pr view` path's three 1-second retries all hit the dead GraphQL
// bucket and the gate FALSE-FAILED a PR that had in fact been created (via the
// skill's own REST fallback) and gone CI-green. Verifying over REST sidesteps
// the exhausted bucket entirely.
//
// Fallbacks (mirrors the resilience already in PrMergeGate):
//   - If REST is unaddressable (no repo slug recorded) or its transport is
//     down, fall back to `gh pr view` (GraphQL).
//   - If BOTH transports are RATE-LIMITED, verification is inconclusive:
//     rather than hard-fail a PR the skill recorded, trust the recorded
//     pr_number and pass with explicit evidence. An environmental rate-limit
//     must never be reported as a code/skill defect — that is the exact #99
//     false-failure. A non-rate-limit failure (404, parse error) is still a
//     genuine gate failure.
type PrCreateGate struct{}

// Name implements StageGate.
func (PrCreateGate) Name() string { return "pr-create" }

// prVerifyOutcome is the conclusion of a single transport's PR lookup.
type prVerifyOutcome int

const (
	prVerifyOpen        prVerifyOutcome = iota // PR exists and is OPEN
	prVerifyNotOpen                            // PR exists but is closed/merged
	prVerifyAbsent                             // transport definitively reports no such PR (404)
	prVerifyRateLimited                        // transport blocked by a GitHub rate limit
	prVerifyError                              // any other transport error (incl. unaddressable)
)

// Verify implements StageGate.
func (PrCreateGate) Verify(ctx context.Context, issueNumber int, workspace string) GateResult {
	return timedKind("pr-create", func() (bool, string, []string, Kind) {
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

		repoSlug := repoSlugFromPRURL(prCtx.PrUrl)

		// Primary: REST (core bucket) — avoids the GraphQL bucket that
		// `gh pr view` consumes.
		restOutcome, restDetail := prStateViaREST(ctx, repoSlug, prCtx.PrNumber)
		if res, ok := classifyPrCreateOutcome(restOutcome, restDetail, prCtx.PrNumber, "REST"); ok {
			return res.passed, res.reason, res.evidence, res.kind
		}

		// Secondary: legacy `gh pr view` (GraphQL). Covers an empty repo slug
		// (REST path unaddressable) or a REST transport that is down while
		// GraphQL is reachable.
		ghOutcome, ghDetail := prStateViaGraphQL(ctx, repoSlug, prCtx.PrNumber)
		if res, ok := classifyPrCreateOutcome(ghOutcome, ghDetail, prCtx.PrNumber, "gh pr view"); ok {
			return res.passed, res.reason, res.evidence, res.kind
		}

		// Both transports were inconclusive. A rate-limit on either is
		// environmental — do NOT hard-fail a PR the skill recorded.
		if restOutcome == prVerifyRateLimited || ghOutcome == prVerifyRateLimited {
			return true, "PR verification deferred — GitHub REST and GraphQL both rate-limited; trusting recorded pr_number", []string{
				fmt.Sprintf("pr=%d", prCtx.PrNumber),
				fmt.Sprintf("rest=%s", truncate(restDetail, 120)),
				fmt.Sprintf("graphql=%s", truncate(ghDetail, 120)),
			}, KindOK
		}
		return false, "PR verification failed over both REST and GraphQL", []string{
			fmt.Sprintf("pr=%d", prCtx.PrNumber),
			fmt.Sprintf("rest=%s", truncate(restDetail, 120)),
			fmt.Sprintf("graphql=%s", truncate(ghDetail, 120)),
		}, KindFail
	})
}

// prCreateRes is a fully-formed gate conclusion for one transport.
type prCreateRes struct {
	passed   bool
	reason   string
	evidence []string
	kind     Kind
}

// classifyPrCreateOutcome maps a single transport's outcome to a terminal gate
// result when that outcome is conclusive (open / not-open / definitively
// absent). Returns ok=false for inconclusive outcomes (rate-limited / error),
// so the caller can try the next transport.
func classifyPrCreateOutcome(outcome prVerifyOutcome, detail string, prNumber int, via string) (prCreateRes, bool) {
	switch outcome {
	case prVerifyOpen:
		return prCreateRes{true, fmt.Sprintf("PR is OPEN with the recorded number (verified via %s)", via), []string{
			fmt.Sprintf("pr=%d", prNumber),
		}, KindOK}, true
	case prVerifyNotOpen:
		return prCreateRes{false, "PR is not OPEN", []string{
			fmt.Sprintf("pr=%d state=%s (via %s)", prNumber, detail, via),
		}, KindNoOp}, true
	case prVerifyAbsent:
		// A definitive 404 — the recorded PR does not exist. This is a genuine
		// error (KindFail), not KindNoOp: the gate's KindNoOp signal is reserved
		// for "missing pr_number" and "PR exists but not open" (matches the
		// pre-REST behavior where an unresolvable PR failed the gh lookup →
		// KindFail, and the synthetic skill-no-op regression guard).
		return prCreateRes{false, "no PR exists for the recorded pr_number", []string{
			fmt.Sprintf("pr=%d (%s via %s)", prNumber, detail, via),
		}, KindFail}, true
	default: // prVerifyRateLimited, prVerifyError
		return prCreateRes{}, false
	}
}

// prStateViaREST looks up the PR over the GitHub REST API (core bucket). The
// REST endpoint reports state as lowercase "open"/"closed"; callers compare
// case-insensitively.
func prStateViaREST(ctx context.Context, repoSlug string, prNumber int) (prVerifyOutcome, string) {
	if repoSlug == "" {
		return prVerifyError, "empty repo slug — REST path unaddressable"
	}
	args := []string{
		"api", fmt.Sprintf("repos/%s/pulls/%d", repoSlug, prNumber),
		"--jq", "{state:.state,number:.number}",
	}
	return prStateViaTransport(ctx, args, prNumber)
}

// prStateViaGraphQL looks up the PR over `gh pr view` (GraphQL bucket). Reports
// state as uppercase "OPEN"/"CLOSED"/"MERGED".
func prStateViaGraphQL(ctx context.Context, repoSlug string, prNumber int) (prVerifyOutcome, string) {
	return prStateViaTransport(ctx, ghPRViewArgs(prNumber, repoSlug, "state,number"), prNumber)
}

// prStateViaTransport runs a `gh` command that returns {state,number} JSON,
// retrying transient failures, and classifies the result. Shared by the REST
// and GraphQL paths so rate-limit / not-found detection stays identical.
func prStateViaTransport(ctx context.Context, args []string, prNumber int) (prVerifyOutcome, string) {
	// Relaxed runs (verified-trivial change, #4128) collapse the 3× rate-limit
	// retry to a single attempt with no sleep.
	attempts := 3
	if Relaxed(ctx) {
		attempts = 1
	}
	var out []byte
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		out, err = execGh(ctx, args...)
		if err == nil && !ghOutputLooksRateLimited(out) {
			break
		}
		if attempt < attempts {
			time.Sleep(1 * time.Second)
		}
	}
	if err != nil || ghOutputLooksRateLimited(out) {
		if ghCallLooksRateLimited(out, err) {
			return prVerifyRateLimited, "rate-limited"
		}
		if ghCallLooksNotFound(out, err) {
			return prVerifyAbsent, "not found"
		}
		detail := "unknown error"
		if err != nil {
			detail = err.Error()
		}
		return prVerifyError, detail
	}

	var resp struct {
		State  string `json:"state"`
		Number int    `json:"number"`
	}
	if e := json.Unmarshal(out, &resp); e != nil {
		return prVerifyError, "unparseable JSON: " + e.Error()
	}
	if resp.Number != 0 && resp.Number != prNumber {
		// Defensive: a transport that resolved a different PR number is a real
		// error, not an absent/rate-limit signal.
		return prVerifyError, fmt.Sprintf("returned different PR number: want=%d got=%d", prNumber, resp.Number)
	}
	if strings.EqualFold(resp.State, "open") {
		return prVerifyOpen, "open"
	}
	return prVerifyNotOpen, strings.ToLower(resp.State)
}

// ghCallLooksRateLimited reports whether a `gh` call failed because of a GitHub
// rate limit, inspecting stdout, the error string, and (for ExitError) stderr —
// `gh` writes the human-readable rate-limit message to stderr, which
// exec.Cmd.Output() surfaces via ExitError.Stderr.
func ghCallLooksRateLimited(out []byte, err error) bool {
	s := ghErrText(out, err)
	return strings.Contains(s, "rate limit") || strings.Contains(s, "secondary rate limit")
}

// ghCallLooksNotFound reports whether a `gh` call failed because the PR does not
// exist (HTTP 404 / could-not-resolve).
func ghCallLooksNotFound(out []byte, err error) bool {
	s := ghErrText(out, err)
	return strings.Contains(s, "not found") ||
		strings.Contains(s, "http 404") ||
		strings.Contains(s, "could not resolve")
}

// ghErrText concatenates stdout, the error message, and any ExitError stderr
// into one lowercased string for substring classification.
func ghErrText(out []byte, err error) string {
	var sb strings.Builder
	sb.Write(out)
	if err != nil {
		sb.WriteString(" ")
		sb.WriteString(err.Error())
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			sb.WriteString(" ")
			sb.Write(ee.Stderr)
		}
	}
	return strings.ToLower(sb.String())
}
