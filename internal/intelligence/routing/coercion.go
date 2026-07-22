// Package routing — CoerceRouting provides deterministic coercion for
// pipeline routing fields read from issue-{N}.json context files.
//
// This mirrors the TypeScript flexEnum + .catch() coercion in
// packages/nightgauge-sdk/src/context/schemas/issue.ts.
// Both layers apply the same rules; the Go layer runs before the TypeScript
// Zod validation to ensure clean input reaches the schema parser.
package routing

import (
	"strings"
)

// validChangeTypes is the authoritative set of change_type values.
// Source of truth: ChangeTypeSchema in packages/.../schemas/issue.ts
var validChangeTypes = map[string]struct{}{
	"docs":   {},
	"config": {},
	"code":   {},
}

// changeTypeAliases mirrors AGENT_ALIASES in helpers.ts for change_type.
var changeTypeAliases = map[string]string{
	"code_change":       "code",
	"code_modification": "code",
	"documentation":     "docs",
	"doc":               "docs",
	"configuration":     "config",
	"conf":              "config",
}

// validRoutes is the authoritative set of suggested_route values.
// Source of truth: RoutingPathSchema in packages/.../schemas/issue.ts
var validRoutes = map[string]struct{}{
	"trivial":   {},
	"standard":  {},
	"extensive": {},
}

// routeAliases mirrors AGENT_ALIASES in helpers.ts for suggested_route.
var routeAliases = map[string]string{
	"trivial_route":   "trivial",
	"quick":           "trivial",
	"simple":          "trivial",
	"extensive_route": "extensive",
	"complex":         "extensive",
	"deep":            "extensive",
}

// validSkipStages is the authoritative set of skip_stages values.
// Source of truth: SkippableStageSchema in packages/.../schemas/issue.ts
var validSkipStages = map[string]struct{}{
	"feature-planning": {},
	"feature-validate": {},
	"pr-create":        {},
	"pr-merge":         {},
}

// CoerceRouting applies deterministic coercion to a raw routing map
// (unmarshaled from issue-{N}.json). Returns the coerced map.
//
// Coercion rules:
//   - complexity_score > 8 → clamped to 8; < 1 → clamped to 1; non-numeric → 3 (M default)
//   - change_type: normalize hyphens→underscores, check alias map, default "code"
//   - suggested_route: normalize hyphens→underscores, check alias map, then
//     recalculate from complexity_score if still invalid: ≤2→trivial, ≥5→extensive, else standard
//   - skip_stages: filter to only known valid values, discard unknowns
//
// Labels are used to improve change_type inference when the raw value is invalid.
// Pass nil if label information is not available.
func CoerceRouting(rawRouting map[string]interface{}, labels []string) map[string]interface{} {
	if rawRouting == nil {
		return rawRouting
	}

	// Work on a shallow copy to avoid mutating the input.
	result := make(map[string]interface{}, len(rawRouting))
	for k, v := range rawRouting {
		result[k] = v
	}

	// RISK_FLOOR (#4093): keep this read-time coercion in lockstep with
	// Derive() — a high-risk issue floors the route at "extensive" and skips no
	// stages, even if the persisted routing map was computed without the risk
	// signal. isHighRisk is the shared source of truth (risk.go).
	highRisk, riskReasons := isHighRisk(labels)

	// --- complexity_score ---
	complexityScore := coerceComplexityScore(result["complexity_score"])
	result["complexity_score"] = complexityScore

	// --- change_type ---
	result["change_type"] = coerceChangeType(result["change_type"], labels)

	// --- suggested_route ---
	result["suggested_route"] = coerceSuggestedRoute(result["suggested_route"], complexityScore, highRisk)

	// --- skip_stages ---
	result["skip_stages"] = coerceSkipStages(result["skip_stages"], highRisk)

	// --- risk fields (surfaced for the discipline score, #4100) ---
	result["risk_high"] = highRisk
	result["risk_reasons"] = riskReasons

	return result
}

// coerceComplexityScore clamps or defaults the complexity_score value.
func coerceComplexityScore(raw interface{}) int {
	switch v := raw.(type) {
	case int:
		return clampComplexity(v)
	case int64:
		return clampComplexity(int(v))
	case float64:
		return clampComplexity(int(v))
	case float32:
		return clampComplexity(int(v))
	default:
		// Missing, nil, or non-numeric — default to M (3)
		return 3
	}
}

func clampComplexity(v int) int {
	if v < 1 {
		return 1
	}
	if v > 8 {
		return 8
	}
	return v
}

// coerceChangeType normalizes and aliases the change_type value.
// Falls back to "code" for unknown values.
func coerceChangeType(raw interface{}, labels []string) string {
	if raw == nil {
		return inferChangeTypeFromLabels(labels)
	}
	s, ok := raw.(string)
	if !ok {
		return inferChangeTypeFromLabels(labels)
	}

	// Step 1: normalize hyphens → underscores and lowercase
	normalized := strings.ToLower(strings.ReplaceAll(s, "-", "_"))

	// Step 2: exact match
	if _, valid := validChangeTypes[normalized]; valid {
		return normalized
	}

	// Step 3: alias map
	if alias, found := changeTypeAliases[normalized]; found {
		return alias
	}

	// Step 4: infer from labels or default
	return inferChangeTypeFromLabels(labels)
}

// inferChangeTypeFromLabels attempts to determine change_type from issue labels.
// Returns "code" as the safe default.
func inferChangeTypeFromLabels(labels []string) string {
	for _, label := range labels {
		lower := strings.ToLower(label)
		if strings.Contains(lower, "docs") || strings.Contains(lower, "documentation") {
			return "docs"
		}
		if strings.Contains(lower, "config") || strings.Contains(lower, "configuration") {
			return "config"
		}
	}
	return "code"
}

// coerceSuggestedRoute normalizes and aliases the suggested_route value.
// Falls back to recalculating from complexityScore if the value is invalid.
func coerceSuggestedRoute(raw interface{}, complexityScore int, highRisk bool) string {
	// RISK_FLOOR (#4093): high-risk floors the route at "extensive", overriding
	// any persisted or alias value.
	if highRisk {
		return "extensive"
	}
	if raw != nil {
		s, ok := raw.(string)
		if ok {
			// Step 1: normalize hyphens → underscores and lowercase
			normalized := strings.ToLower(strings.ReplaceAll(s, "-", "_"))

			// Step 2: exact match
			if _, valid := validRoutes[normalized]; valid {
				return normalized
			}

			// Step 3: alias map
			if alias, found := routeAliases[normalized]; found {
				return alias
			}
		}
	}

	// Step 4: recalculate from complexity_score
	return routeFromComplexity(complexityScore)
}

// routeFromComplexity returns the canonical route for a given complexity score.
// Mirrors determineRoutingPath() logic in changeAnalyzer.ts.
func routeFromComplexity(score int) string {
	if score <= 2 {
		return "trivial"
	}
	if score >= 5 {
		return "extensive"
	}
	return "standard"
}

// coerceSkipStages filters the skip_stages array to only valid stage names.
// Invalid stage names are silently discarded.
func coerceSkipStages(raw interface{}, highRisk bool) []string {
	// RISK_FLOOR (#4093): high-risk forces the full pipeline — no stage is
	// skipped, mirroring skipStagesFor() in derive.go.
	if highRisk {
		return []string{}
	}
	if raw == nil {
		return []string{}
	}

	arr, ok := raw.([]interface{})
	if !ok {
		// Already typed as []string (e.g. from direct Go construction)
		if typed, ok2 := raw.([]string); ok2 {
			return filterSkipStages(typed)
		}
		return []string{}
	}

	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			normalized := strings.ToLower(s)
			if _, valid := validSkipStages[normalized]; valid {
				result = append(result, normalized)
			}
		}
	}
	return result
}

// filterSkipStages filters a []string to only valid skip stage values.
func filterSkipStages(stages []string) []string {
	result := make([]string, 0, len(stages))
	for _, s := range stages {
		normalized := strings.ToLower(s)
		if _, valid := validSkipStages[normalized]; valid {
			result = append(result, normalized)
		}
	}
	return result
}
