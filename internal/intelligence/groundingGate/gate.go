// Package groundingGate implements the pre-feature-dev grounding /
// context-sufficiency gate (#4099).
//
// docs/GUARDRAILS_AND_BUDGETS.md (the #3863 worked example) names the exact gap
// this closes: "No pre-action 'am I on the right issue/branch?' grounding
// check." Before feature-dev burns tokens, the gate deterministically confirms
// the agent is grounded — on the issue's feature branch (not main), with the
// issue context actually present — and flags an under-specified premise so the
// skill can re-ground or pull human context instead of acting on a hallucinated
// premise.
//
// Evaluate is pure (no IO, no clock); the CLI gathers the git/context facts and
// feeds them in, mirroring the scopeDriftGate split.
package groundingGate

import (
	"fmt"
	"strings"
)

// GroundingInput is the set of deterministic facts the gate reasons over.
type GroundingInput struct {
	IssueNumber       int
	CurrentBranch     string   // `git rev-parse --abbrev-ref HEAD`
	ExpectedBranch    string   // issue-{N}.json .branch (empty = unknown)
	ContextPresent    bool     // issue-{N}.json existed and parsed
	ACCount           int      // acceptance_criteria length in context
	ProtectedBranches []string // branches feature-dev must never edit directly (main/master/base)
}

// GroundingResult is the gate verdict.
type GroundingResult struct {
	Grounded       bool     `json:"grounded"`
	Confidence     string   `json:"confidence"`     // high | medium | low
	Recommendation string   `json:"recommendation"` // proceed | re-ground | pull-human
	Reasons        []string `json:"reasons,omitempty"`
}

// Evaluate returns the grounding verdict for the given facts.
//
//   - Hard ungrounded (Grounded=false → "re-ground"): missing context, on a
//     protected branch, or current branch != the issue's expected branch. These
//     are the hallucinated-task / lost-grounding signals from #3863 — acting on
//     them edits the wrong place.
//   - Grounded but low-confidence ("pull-human"): no acceptance criteria — the
//     premise is under-specified; consider pulling human context before acting.
//   - Grounded + high-confidence ("proceed").
func Evaluate(in GroundingInput) GroundingResult {
	var reasons []string
	grounded := true

	if !in.ContextPresent {
		grounded = false
		reasons = append(reasons,
			fmt.Sprintf("issue #%d context (issue-%d.json) missing or unparseable — re-run issue-pickup", in.IssueNumber, in.IssueNumber))
	}

	for _, p := range in.ProtectedBranches {
		if p != "" && strings.EqualFold(in.CurrentBranch, p) {
			grounded = false
			reasons = append(reasons,
				fmt.Sprintf("on protected branch %q — feature-dev must run on the issue's feature branch, not the base", in.CurrentBranch))
			break
		}
	}

	if grounded && in.ExpectedBranch != "" && in.CurrentBranch != "" &&
		!strings.EqualFold(in.CurrentBranch, in.ExpectedBranch) {
		grounded = false
		reasons = append(reasons,
			fmt.Sprintf("current branch %q != issue #%d branch %q — am I on the right issue?", in.CurrentBranch, in.IssueNumber, in.ExpectedBranch))
	}

	confidence := "high"
	recommendation := "proceed"

	if !grounded {
		confidence = "low"
		recommendation = "re-ground"
		return GroundingResult{Grounded: false, Confidence: confidence, Recommendation: recommendation, Reasons: reasons}
	}

	if in.ACCount == 0 {
		confidence = "low"
		recommendation = "pull-human"
		reasons = append(reasons, "no acceptance criteria in context — premise may be under-specified; consider pulling human context")
	}

	return GroundingResult{Grounded: grounded, Confidence: confidence, Recommendation: recommendation, Reasons: reasons}
}
