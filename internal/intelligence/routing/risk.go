// Package routing — risk.go classifies an issue as high-risk from its labels so
// routing can force the full pipeline regardless of complexity score.
//
// Rationale (issue #4093): complexity_score tracks SIZE, not blast radius. A
// small (low-complexity) change to security, auth, billing, a migration, or a
// public API is exactly where skipping feature-planning/feature-validate is most
// dangerous — and exactly where the downstream verification gates (#4097,
// #4098, #4099) must still run. isHighRisk is the single source of truth shared
// by derive.go and coercion.go; the TypeScript mirror lives in
// packages/nightgauge-vscode/src/utils/changeAnalyzer.ts (isHighRisk).
//
// The signal is LABEL-based only — no diff/blast-radius information is plumbed
// into the Derive path. Over-classification is intentionally safe: it only adds
// rigor (forces the full pipeline), never removes it.
package routing

import "strings"

// riskKeywords are matched as case-insensitive substrings against each raw
// label slug. MUST stay byte-identical to RISK_KEYWORDS in
// packages/nightgauge-vscode/src/utils/changeAnalyzer.ts.
var riskKeywords = []string{
	"security",
	"auth",
	"billing",
	"payment",
	"migration",
	"public-api",
	"breaking",
	"credential",
}

// riskEscapeHatchLabels force a high-risk classification regardless of keyword
// matching, letting a human flag an otherwise-innocuous issue. MUST mirror
// RISK_ESCAPE_HATCH in changeAnalyzer.ts.
var riskEscapeHatchLabels = map[string]struct{}{
	"risk:high": {},
	"risk-high": {},
}

// isHighRisk reports whether the labels mark the issue as high blast-radius and
// returns the de-duplicated matched label slugs as reasons. Pure and
// deterministic: identical input always yields identical output.
func isHighRisk(labels []string) (bool, []string) {
	reasons := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(s string) {
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		reasons = append(reasons, s)
	}

	for _, l := range labels {
		lower := strings.ToLower(strings.TrimSpace(l))
		if _, ok := riskEscapeHatchLabels[lower]; ok {
			add(lower)
			continue
		}
		for _, kw := range riskKeywords {
			if strings.Contains(lower, kw) {
				add(lower)
				break
			}
		}
	}

	return len(reasons) > 0, reasons
}
