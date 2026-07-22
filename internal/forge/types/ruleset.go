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
	BaseRef        string   `json:"base_ref"`
	AllowedToMerge bool     `json:"allowed_to_merge"`
	Message        string   `json:"message"`
}
