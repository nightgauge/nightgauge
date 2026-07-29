package forgetypes

// RulesetCheckResult captures the precheck outcome for a forge's branch
// protection / merge rulesets. After a successful auto-satisfy pass,
// Blockers reflects the unresolved subset; ResolvedBlockers names the
// auto-satisfied rules.
type RulesetCheckResult struct {
	Blockers         []string `json:"blockers"`
	DetectedRules    []string `json:"detected_rules"`
	ResolvedBlockers []string `json:"resolved_blockers,omitempty"`
	// RequiredChecks lists status-check contexts enforced by rulesets on the
	// base branch. They are not Blockers (a green run satisfies them), but
	// callers must wait on / verify them — historically these were invisible
	// and merges looped against "No required status checks found" (#184).
	RequiredChecks []string `json:"required_checks,omitempty"`
	// BypassedRules names rules that were detected on the base branch but do
	// NOT block this merge, because the authenticated identity holds a bypass
	// on the ruleset carrying them. They are reported for transparency —
	// "detected but not binding on me" is a different statement from "not
	// present" and an operator reading a precheck deserves to see it.
	//
	// Omitting this distinction is what made a bypassable rule read as a hard
	// block, and callers promote a hard block to a NON-RETRYABLE terminal
	// failure — killing runs over a merge that would in fact have succeeded.
	BypassedRules  []string `json:"bypassed_rules,omitempty"`
	BaseRef        string   `json:"base_ref"`
	AllowedToMerge bool     `json:"allowed_to_merge"`
	Message        string   `json:"message"`
}
