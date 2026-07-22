// Package approvalGate implements the default-on architecture-approval gate
// (#4098): high-impact architectural decisions stay human-owned.
//
// The "New SDLC" whitepaper's sharpest claim is that hard trade-offs
// (consistency-vs-availability, build-vs-buy, reuse-vs-build) need business
// context an agent cannot fully grasp, so a human MUST make/approve them. Today
// the agent both makes AND self-stamps ADRs (`Status: Accepted`), and with
// `approvalGates: []` a wrong trade-off merges freely once tests pass.
//
// This gate is deliberately a DETERMINISTIC gate, not an interactive
// "approve this stage?" prompt — an interactive prompt would be swallowed by
// `human_in_the_loop.auto_accept_stages: true`. As a hard gate it blocks
// (exit non-zero) until a human grants approval out-of-band (a label or an
// approval file), so it is exempt from stage auto-accept by construction.
//
// Evaluate is pure; the CLI gathers the issue/ADR text + the approval signal.
package approvalGate

import (
	"fmt"
	"strings"
)

// tradeoffSignals are distinct architectural trade-off phrases. Two or more
// distinct matches in the issue/ADR text marks a high-impact decision. Matched
// case-insensitively as substrings.
var tradeoffSignals = []string{
	"consistency vs", "availability vs", "cap theorem",
	"build vs buy", "buy vs build", "build-vs-buy",
	"reuse vs build", "reuse vs. build", "reuse vs build new",
	"sync vs async", "synchronous vs asynchronous",
	"monolith vs", "vs microservice", "microservices vs",
	"sql vs nosql", "relational vs", "vs relational",
	"rest vs graphql", "graphql vs rest",
	"backwards compat", "backward compat", "breaking change",
	"vendor lock", "tightly coupled", "coupling vs",
}

// CountTradeoffSignals returns how many DISTINCT trade-off phrases appear in
// text (case-insensitive). Distinct so repeating one phrase does not inflate
// the score.
func CountTradeoffSignals(text string) int {
	lower := strings.ToLower(text)
	n := 0
	for _, sig := range tradeoffSignals {
		if strings.Contains(lower, sig) {
			n++
		}
	}
	return n
}

// ApprovalInput is the deterministic facts the gate reasons over.
type ApprovalInput struct {
	IssueNumber         int
	TradeoffKeywordHits int  // distinct CountTradeoffSignals over issue + ADR text
	RiskHigh            bool // routing.risk_high (#4093)
	// DependencyMajorBumpCount is the number of dependency MAJOR-version bumps
	// the change introduces (#4135), sourced from feature-planning's
	// dependency_analysis.major_bumps_count fact. > 0 marks the decision
	// high-impact: a major bump is an irreversible-once-merged API break that
	// merits a human checkpoint. Absence (0) never fires — the fact must be
	// present and positive to avoid a false-positive flood.
	DependencyMajorBumpCount int
	// IsProductionChange marks a production-touching change (#4135), sourced
	// from feature-planning's dependency_analysis.production_area fact. true
	// marks the decision high-impact.
	IsProductionChange bool
	ApprovalGranted    bool // a human approval signal (label or file) is present
}

// ApprovalResult is the gate verdict.
type ApprovalResult struct {
	HighImpact       bool     `json:"high_impact"`
	RequiresApproval bool     `json:"requires_approval"`
	ApprovalGranted  bool     `json:"approval_granted"`
	Reasons          []string `json:"reasons,omitempty"`
}

// TradeoffThreshold is the number of distinct trade-off signals that, on their
// own, mark a decision high-impact.
const TradeoffThreshold = 2

// Evaluate returns whether the decision is high-impact and, if so, whether it
// still needs human approval. High-impact = (≥ TradeoffThreshold distinct
// trade-off signals) OR a high-risk blast radius. A high-impact decision that
// has not been human-approved requires approval (the gate blocks).
func Evaluate(in ApprovalInput) ApprovalResult {
	var reasons []string
	highImpact := false

	if in.TradeoffKeywordHits >= TradeoffThreshold {
		highImpact = true
		reasons = append(reasons,
			fmt.Sprintf("%d distinct architectural trade-off signals (consistency/availability, build/buy, reuse/build, …)", in.TradeoffKeywordHits))
	}
	if in.RiskHigh {
		highImpact = true
		reasons = append(reasons, "high-risk blast radius (routing risk_high, #4093)")
	}
	if in.DependencyMajorBumpCount > 0 {
		highImpact = true
		reasons = append(reasons,
			fmt.Sprintf("%d dependency major-version bump(s) — an irreversible-once-merged API break (#4135)", in.DependencyMajorBumpCount))
	}
	if in.IsProductionChange {
		highImpact = true
		reasons = append(reasons, "production-touching change — irreversible blast radius (#4135)")
	}

	res := ApprovalResult{HighImpact: highImpact, ApprovalGranted: in.ApprovalGranted}
	if highImpact && !in.ApprovalGranted {
		res.RequiresApproval = true
		reasons = append(reasons,
			"architecture not yet human-approved — a human must review the decision and grant approval (label or approval file) before feature-dev proceeds")
	}
	res.Reasons = reasons
	return res
}
