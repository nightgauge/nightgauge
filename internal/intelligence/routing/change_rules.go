// change_rules.go — routing.change_rules: the user-customizable mapping from
// file globs (and/or coarse change_types) to fast-track behavior. This is the
// single Go source of truth for the shape mirrored by the TypeScript
// ChangeRuleSchema (packages/nightgauge-vscode/src/config/schema.ts) and
// embedded in the config loader (internal/config RoutingConfig).
//
// Two layers consume the same rule with different matching modes:
//
//   - Predictive (Derive(), issue-pickup time): there is no diff yet, so rules
//     are matched by ChangeTypes against the derived change_type. Glob-only
//     rules (no ChangeTypes) are invisible here — they are deferred to the
//     authoritative layer.
//   - Authoritative (scheduler / CI, post-dev): the real changed files are
//     matched against Globs via scopeDriftGate.MatchPath. Wired in #4126/#4127.
//
// @see Issue #4125
package routing

import "strings"

// ChangeRule is one routing.change_rules entry.
type ChangeRule struct {
	// Name uniquely identifies the rule. A user rule whose Name equals a
	// built-in default's Name replaces that default (see MergeChangeRules).
	Name string `json:"name" yaml:"name"`
	// Description is human-facing documentation; ignored by matching.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Globs are gitignore-style patterns (scopeDriftGate syntax: "dir/**",
	// "**/suffix", segment-anchored globs) that select changed files for
	// authoritative post-dev matching.
	Globs []string `json:"globs,omitempty" yaml:"globs,omitempty"`
	// ChangeTypes restricts predictive matching to these coarse change kinds
	// ("code" | "docs" | "config"). Empty means the rule never matches
	// predictively — it is a glob-only authoritative rule (deferred to #4126).
	ChangeTypes []string `json:"change_types,omitempty" yaml:"change_types,omitempty"`
	// SkipStages replaces the complexity-derived skip list when the rule wins.
	SkipStages []string `json:"skip_stages,omitempty" yaml:"skip_stages,omitempty"`
	// CIJobs names the CI jobs the matched change is allowed to run (consumed by
	// the CI fast-track, #4127). Ignored by Derive().
	CIJobs []string `json:"ci_jobs,omitempty" yaml:"ci_jobs,omitempty"`
	// OverrideRoute, when non-empty and valid, replaces the complexity-derived
	// route ("trivial" | "standard" | "extensive").
	OverrideRoute string `json:"override_route,omitempty" yaml:"override_route,omitempty"`
}

// DefaultChangeRules returns the built-in fast-track rules applied when a repo
// declares none. They mirror changeClassifier.DefaultClassPatterns (#4124) so
// the predictive and authoritative layers agree on what "docs" / "config" mean.
//
//   - docs-only: documentation changes skip planning + validate, route trivial.
//   - config-only: configuration changes skip validate, route trivial.
//   - high-risk-floor: blast-radius paths force the extensive route with no
//     skips. Glob-only (no ChangeTypes) — it is an authoritative post-dev guard
//     (#4126); the predictive risk floor is already enforced by
//     isHighRisk(labels), which outranks every rule.
func DefaultChangeRules() []ChangeRule {
	return []ChangeRule{
		{
			Name:          "docs-only",
			Description:   "Documentation-only changes skip planning and validation.",
			Globs:         []string{"docs/**", "**/*.md", "**/*.mdx", "README*", "CHANGELOG*", "LICENSE*"},
			ChangeTypes:   []string{"docs"},
			SkipStages:    []string{"feature-planning", "feature-validate"},
			OverrideRoute: "trivial",
		},
		{
			Name:          "config-only",
			Description:   "Configuration-only changes skip validation.",
			Globs:         []string{".nightgauge/**", ".github/**", "**/*.yaml", "**/*.yml"},
			ChangeTypes:   []string{"config"},
			SkipStages:    []string{"feature-validate"},
			OverrideRoute: "trivial",
		},
		{
			Name:          "high-risk-floor",
			Description:   "Blast-radius paths force the full extensive pipeline with no stage skipping.",
			Globs:         []string{"**/auth/**", "**/payments/**", "**/billing/**", "migrations/**"},
			OverrideRoute: "extensive",
		},
	}
}

// MergeChangeRules layers user rules over the built-in defaults. A user rule
// whose Name matches a default replaces it; user rules with new names are
// emitted ahead of the remaining defaults so they are matched first. The result
// preserves first-match-wins precedence: user rules (overrides + new), then the
// untouched defaults in their canonical order. When user is empty the defaults
// are returned unchanged.
func MergeChangeRules(user []ChangeRule) []ChangeRule {
	defaults := DefaultChangeRules()
	if len(user) == 0 {
		return defaults
	}

	// Collapse duplicate user names (last definition wins) while preserving the
	// order of first appearance.
	byName := make(map[string]ChangeRule, len(user))
	order := make([]string, 0, len(user))
	for _, r := range user {
		if _, seen := byName[r.Name]; !seen {
			order = append(order, r.Name)
		}
		byName[r.Name] = r
	}

	// Avoid deriving an allocation size from two independently sized, user-
	// controlled slices. append grows the result with overflow checks.
	var merged []ChangeRule
	emitted := make(map[string]bool, len(order))
	for _, name := range order {
		merged = append(merged, byName[name])
		emitted[name] = true
	}
	for _, d := range defaults {
		if emitted[d.Name] {
			continue // overridden by a user rule of the same name
		}
		merged = append(merged, d)
	}
	return merged
}

// matchChangeRulePredictive returns the first rule whose ChangeTypes include
// changeType. Glob-only rules (empty ChangeTypes) never match here — at
// Derive() time there is no diff to match globs against, so those rules are
// deferred to the authoritative post-dev layer (#4126). Rules are expected to
// already be in precedence order (see MergeChangeRules).
func matchChangeRulePredictive(rules []ChangeRule, changeType string) (ChangeRule, bool) {
	target := strings.ToLower(strings.TrimSpace(changeType))
	for _, r := range rules {
		for _, ct := range r.ChangeTypes {
			if strings.ToLower(strings.TrimSpace(ct)) == target {
				return r, true
			}
		}
	}
	return ChangeRule{}, false
}

// validOverrideRoute reports whether route is one of the canonical route names.
// An invalid override_route is ignored (the complexity-derived route stands)
// rather than silently corrupting the decision.
func validOverrideRoute(route string) bool {
	_, ok := validRoutes[strings.ToLower(strings.TrimSpace(route))]
	return ok
}
