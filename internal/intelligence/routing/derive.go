// Package routing — Derive consolidates the pipeline-routing decisions that
// were previously re-derived in shell across two pipeline skills:
//   - skills/nightgauge-issue-pickup/SKILL.md Step 3.2.5
//   - skills/nightgauge-feature-planning/SKILL.md Phase 2
//
// The pure function Derive(DeriveInput) Decision mirrors the canonical
// algorithm implemented in TypeScript at
// packages/nightgauge-vscode/src/utils/changeAnalyzer.ts so observed
// routing decisions are unchanged. Reuse-, not rebuild-, of the shared
// constants in coercion.go (validSkipStages, routeFromComplexity,
// inferChangeTypeFromLabels) keeps the valid-value sets in lockstep with
// the SDK Zod schemas in
// packages/nightgauge-sdk/src/context/schemas/issue.ts.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B4.
package routing

import (
	"fmt"
	"regexp"
	"strings"
)

// DeriveInput captures the issue metadata required to derive a routing
// Decision. All fields are optional — callers that lack project-board
// values pass empty strings and the derivation falls back to label-based
// inference.
type DeriveInput struct {
	Title         string
	Body          string
	Labels        []string // raw label slugs (e.g. "type:feature", "size:M", "priority:high")
	BoardSize     string   // "XS"|"S"|"M"|"L"|"XL" — empty if unset
	BoardPriority string   // "P0"|"P1"|"P2"|"P3" — empty if unset

	// ForceFullPipeline mirrors routing.force_full_pipeline. When true, all
	// stage skipping and rule-based overrides are disabled — every stage runs.
	// Precedence #2, below the label-based risk floor and above change_rules.
	ForceFullPipeline bool
	// ChangeRules are the user-configured routing.change_rules entries. They are
	// layered over the built-in defaults (see MergeChangeRules) and consulted
	// predictively by ChangeTypes — at Derive() time there is no diff, so
	// glob-only rules are deferred to the authoritative post-dev layer (#4126).
	ChangeRules []ChangeRule
}

// Decision is the pipeline-routing verdict emitted by Derive.
type Decision struct {
	ChangeType         string   `json:"change_type"`
	TaskType           string   `json:"task_type"`
	ComplexityScore    int      `json:"complexity_score"`
	SuggestedRoute     string   `json:"suggested_route"`
	SkipStages         []string `json:"skip_stages"`
	FoundationTask     bool     `json:"foundation_task"`
	DocumentationScope string   `json:"documentation_scope"`
	Rationale          string   `json:"rationale"`
	EffectiveSize      string   `json:"effective_size"`
	EffectivePriority  string   `json:"effective_priority"`
	// RiskHigh is true when label-based risk classification forced the full
	// pipeline + extensive route regardless of complexity score (#4093).
	RiskHigh bool `json:"risk_high"`
	// RiskReasons holds the label slugs that triggered the high-risk
	// classification (consumed downstream by the discipline score, #4100).
	RiskReasons []string `json:"risk_reasons"`
	// MatchedChangeRule names the routing.change_rules entry that overrode the
	// complexity-derived route / skip_stages, or "" when none matched (#4125).
	MatchedChangeRule string `json:"matched_change_rule,omitempty"`
}

// Derive returns the deterministic routing Decision for an issue.
// The function is pure: identical inputs always produce identical outputs.
func Derive(in DeriveInput) Decision {
	taskType := deriveTaskType(in.Labels, in.Title, in.Body)
	foundation := detectFoundationTask(taskType, in.Title)

	effectiveSize := resolveSize(in.BoardSize, in.Labels, foundation)
	effectivePriority := resolvePriority(in.BoardPriority, in.Labels)

	changeType := deriveChangeType(in.Labels, in.Title, in.Body)
	complexity := complexityFromSize(effectiveSize, effectivePriority, changeType)

	// RISK_FLOOR invariant (#4093): a label-based high-risk classification forces
	// the full pipeline (no stage skipping) and floors the route at "extensive",
	// regardless of complexity_score. complexity tracks size, not blast radius —
	// so a small change to security/auth/billing/a migration/a public API must
	// still run feature-planning + feature-validate (and the gates that hang off
	// them). feature-dev is already non-skippable everywhere, so it needs no
	// extra protection here.
	highRisk, riskReasons := isHighRisk(in.Labels)

	route := routeForDecision(changeType, complexity, effectiveSize, effectivePriority, highRisk)
	skip := skipStagesFor(taskType, complexity, foundation, changeType, highRisk)
	docScope := documentationScopeFor(effectiveSize, taskType, effectivePriority)

	// change_rules precedence (#4125):
	//   1. risk_high floor (already baked into route/skip above) — outranks all.
	//   2. force_full_pipeline — disables every skip; no rule may fast-track.
	//   3. first matching change_rule (user rule, then built-in default) —
	//      its skip_stages REPLACE the complexity-derived list, and a valid
	//      override_route REPLACES the route.
	//   4. otherwise: the complexity-derived route/skip stand.
	// First-match-wins; matching here is predictive (by change_type) because no
	// diff exists at issue-pickup time.
	matchedRule := ""
	switch {
	case highRisk:
		// Risk floor already applied; rules must not relax a high-risk change.
	case in.ForceFullPipeline:
		skip = []string{} // run every stage; ignore rules entirely
	default:
		rules := MergeChangeRules(in.ChangeRules)
		if r, ok := matchChangeRulePredictive(rules, changeType); ok {
			matchedRule = r.Name
			skip = filterSkipStages(append([]string{}, r.SkipStages...))
			if validOverrideRoute(r.OverrideRoute) {
				route = strings.ToLower(strings.TrimSpace(r.OverrideRoute))
			}
		}
	}

	return Decision{
		ChangeType:         changeType,
		TaskType:           taskType,
		ComplexityScore:    complexity,
		SuggestedRoute:     route,
		SkipStages:         skip,
		FoundationTask:     foundation,
		DocumentationScope: docScope,
		Rationale:          buildRationale(route, changeType, complexity, effectiveSize, effectivePriority, taskType, foundation, highRisk),
		EffectiveSize:      effectiveSize,
		EffectivePriority:  effectivePriority,
		RiskHigh:           highRisk,
		RiskReasons:        riskReasons,
		MatchedChangeRule:  matchedRule,
	}
}

// --- task type detection ---

// deriveTaskType returns one of: verification | docs-only | bugfix | refactor |
// chore | feature. Mirrors detectTaskType() in changeAnalyzer.ts; labels are
// the most reliable signal so they take precedence over content heuristics.
func deriveTaskType(labels []string, title, body string) string {
	switch labelTypeOf(labels) {
	case "verification":
		return "verification"
	case "docs":
		return "docs-only"
	case "bug":
		return "bugfix"
	case "refactor":
		return "refactor"
	case "chore", "test":
		// test tasks are routed like chores (skip planning).
		return "chore"
	case "feature":
		return "feature"
	}

	content := strings.ToLower(title + " " + body)
	if matchesAny(content, verificationPatterns) {
		return "verification"
	}
	if matchesAny(content, docsPatterns) && !hasCodeIndicator(content) {
		return "docs-only"
	}
	return "feature"
}

// labelTypeOf extracts the type:* slug, lowercased. Returns "" when no
// type label is present.
func labelTypeOf(labels []string) string {
	for _, l := range labels {
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "type:") {
			return strings.TrimPrefix(lower, "type:")
		}
	}
	return ""
}

var (
	verificationPatterns = []string{"verify", "confirm", "audit"}
	docsPatterns         = []string{"document", "docs", "readme", "changelog"}
	codeIndicatorRE      = regexp.MustCompile(`(?i)\b(implement|fix|add|create|update|refactor)\s+(function|class|method|api|endpoint|component|service)`)
)

func matchesAny(content string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(content, n) {
			return true
		}
	}
	return false
}

func hasCodeIndicator(content string) bool {
	return codeIndicatorRE.MatchString(content)
}

// --- foundation task detection ---

// foundationTitleRE matches the SKILL.md regex
// `\b(scaffold|setup|bootstrap|initialize|initialise|init|configure)\b`.
var foundationTitleRE = regexp.MustCompile(`(?i)\b(scaffold|setup|bootstrap|initialize|initialise|init|configure)\b`)

// detectFoundationTask returns true for type:chore issues whose titles
// match a scaffold/setup keyword. Foundation tasks force trivial routing
// (skip planning + validate) per Issue #1318.
func detectFoundationTask(taskType, title string) bool {
	return taskType == "chore" && foundationTitleRE.MatchString(title)
}

// --- size + priority resolution ---

// resolveSize returns "XS"|"S"|"M"|"L"|"XL". Order: foundation override →
// board field → size:* label → default M.
func resolveSize(boardSize string, labels []string, foundation bool) string {
	if foundation {
		return "XS"
	}
	if normalized := normalizeSize(boardSize); normalized != "" {
		return normalized
	}
	for _, l := range labels {
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "size:") {
			if normalized := normalizeSize(strings.TrimPrefix(lower, "size:")); normalized != "" {
				return normalized
			}
		}
	}
	return "M"
}

func normalizeSize(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "XS", "S", "M", "L", "XL":
		return strings.ToUpper(strings.TrimSpace(s))
	}
	return ""
}

// resolvePriority returns "critical"|"high"|"medium"|"low" or "" when
// neither board priority nor a priority:* label is present.
func resolvePriority(boardPriority string, labels []string) string {
	switch strings.ToUpper(strings.TrimSpace(boardPriority)) {
	case "P0":
		return "critical"
	case "P1":
		return "high"
	case "P2":
		return "medium"
	case "P3":
		return "low"
	}
	for _, l := range labels {
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "priority:") {
			val := strings.TrimPrefix(lower, "priority:")
			switch val {
			case "critical", "high", "medium", "low":
				return val
			}
		}
	}
	return ""
}

// --- complexity score ---

// sizeBaseScore mirrors SIZE_COMPLEXITY_MAP in changeAnalyzer.ts.
var sizeBaseScore = map[string]int{
	"XS": 1,
	"S":  2,
	"M":  3,
	"L":  5,
	"XL": 8,
}

// priorityMultiplier mirrors PRIORITY_MULTIPLIER in changeAnalyzer.ts.
var priorityMultiplier = map[string]float64{
	"critical": 1.5,
	"high":     1.2,
	"medium":   1.0,
	"low":      0.8,
}

var fibonacci = []int{1, 2, 3, 5, 8}

// complexityFromSize returns the Fibonacci complexity score for a given
// size+priority combination. Non-code changes (docs/config) cap the base
// score at 2 before multiplier application — matches the TypeScript
// implementation so observed routing is unchanged.
func complexityFromSize(size, priority, changeType string) int {
	base, ok := sizeBaseScore[size]
	if !ok {
		base = 3
	}
	if changeType == "docs" || changeType == "config" {
		if base > 2 {
			base = 2
		}
	}

	mult := 1.0
	if m, ok := priorityMultiplier[priority]; ok {
		mult = m
	}
	adjusted := float64(base) * mult

	closest := fibonacci[0]
	minDiff := abs(adjusted - float64(closest))
	for _, f := range fibonacci[1:] {
		diff := abs(adjusted - float64(f))
		if diff < minDiff {
			minDiff = diff
			closest = f
		}
	}
	return closest
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// --- change type ---

// deriveChangeType combines the canonical TS detection logic with the
// existing label-only helper from coercion.go so callers get the same
// fallback ladder used elsewhere in the binary.
func deriveChangeType(labels []string, title, body string) string {
	switch labelTypeOf(labels) {
	case "docs":
		return "docs"
	case "feature", "bug", "refactor", "spike":
		return "code"
	}

	content := strings.ToLower(title + " " + body)
	docsLike := matchesAny(content, docsPatterns)
	if docsLike && !hasCodeIndicator(content) {
		return "docs"
	}

	configLike := matchesAny(content, []string{".yaml", ".yml", ".json", ".toml", "config", ".env"})
	featureLike := matchesAny(content, []string{"feature", "functionality", "behavior", "user can", "should be able"})
	if configLike && !featureLike && !docsLike {
		return "config"
	}

	// Final fallback shares logic with coercion.go so the two stay in sync.
	return inferChangeTypeFromLabels(labels)
}

// --- route selection ---

// routeForDecision returns "trivial" | "standard" | "extensive". Mirrors
// determineRoutingPath() in changeAnalyzer.ts: docs+small or complexity≤2 →
// trivial; large size, critical priority, or complexity≥5 → extensive;
// otherwise standard.
func routeForDecision(changeType string, complexity int, size, priority string, highRisk bool) string {
	// RISK_FLOOR (#4093): high-risk floors the route at the most thorough path,
	// overriding any docs/trivial/standard downgrade.
	if highRisk {
		return "extensive"
	}
	isNonCode := changeType == "docs" || changeType == "config"
	isTrivialSize := size == "XS" || size == "S"
	if (isNonCode && isTrivialSize) || complexity <= 2 {
		return "trivial"
	}
	isLargeSize := size == "L" || size == "XL"
	isCritical := priority == "critical"
	if isLargeSize || isCritical || complexity >= 5 {
		return "extensive"
	}
	return "standard"
}

// --- skip stages ---

// skipStagesFor returns the set of stages a route can safely skip.
// Mirrors determineSkipStages() in changeAnalyzer.ts. Output is filtered
// against validSkipStages (defined in coercion.go) so it stays in lockstep
// with SkippableStageSchema in the SDK.
func skipStagesFor(taskType string, complexity int, foundation bool, changeType string, highRisk bool) []string {
	// RISK_FLOOR (#4093): high-risk forces the full pipeline — nothing is
	// skipped, so feature-planning and feature-validate (and the verification
	// gates that hang off them) always run on high blast-radius changes.
	if highRisk {
		return []string{}
	}

	stages := make([]string, 0, 4)
	add := func(s string) {
		for _, existing := range stages {
			if existing == s {
				return
			}
		}
		stages = append(stages, s)
	}

	if complexity <= 2 {
		add("feature-planning")
		add("feature-validate")
	}
	if taskType == "chore" {
		add("feature-planning")
	}
	if foundation {
		add("feature-planning")
		add("feature-validate")
	}
	if changeType == "docs" || changeType == "config" {
		add("feature-validate")
	}
	if taskType == "docs-only" {
		add("feature-validate")
	}

	return filterSkipStages(stages)
}

// --- documentation scope ---

// documentationScopeFor mirrors the feature-planning Phase 2 decision tree:
//   - XS + bug → minimal
//   - S + bug or docs → targeted
//   - L/XL or critical → extended
//   - else → standard
func documentationScopeFor(size, taskType, priority string) string {
	if size == "XS" && taskType == "bugfix" {
		return "minimal"
	}
	if size == "S" && (taskType == "bugfix" || taskType == "docs-only") {
		return "targeted"
	}
	if size == "L" || size == "XL" || priority == "critical" {
		return "extended"
	}
	return "standard"
}

// --- rationale ---

func buildRationale(route, changeType string, complexity int, size, priority, taskType string, foundation, highRisk bool) string {
	parts := []string{}
	if size != "" {
		parts = append(parts, size+" size")
	}
	parts = append(parts, changeType+" change")
	parts = append(parts, fmt.Sprintf("complexity %d", complexity))
	if priority != "" {
		parts = append(parts, priority+" priority")
	}

	suffix := ""
	switch route {
	case "trivial":
		suffix = "Trivial path: " + strings.Join(parts, ", ") + ". Skipping planning + validate."
	case "extensive":
		suffix = "Extensive path: " + strings.Join(parts, ", ") + ". Full pipeline with extended documentation."
	default:
		suffix = "Standard path: " + strings.Join(parts, ", ") + ". Full pipeline execution."
	}
	if foundation {
		suffix = "Foundation task — " + suffix
	}
	if taskType != "feature" {
		suffix = strings.TrimSuffix(suffix, ".") + " (task: " + taskType + ")."
	}
	if highRisk {
		suffix = "High-risk — forced extensive route + full pipeline. " + suffix
	}
	return suffix
}
