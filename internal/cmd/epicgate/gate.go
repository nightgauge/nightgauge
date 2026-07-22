// Package epicgate mirrors the Phase 2.9 epic-decomposition detection logic
// from skills/nightgauge-issue-create/SKILL.md (lines 875-1012) as a
// pure Go function so it can be unit-tested independently of the bash gate.
// The bash gate in SKILL.md remains the production consumer; this package
// exists solely to enable regression testing of the classification rules.
package epicgate

import (
	"regexp"
	"strings"
)

// EpicShape classifies an epic creation attempt.
type EpicShape string

const (
	// ShapeA — sub-issues were planned (subIssueCount > 0).
	ShapeA EpicShape = "path_a"
	// ShapeB — explicit placeholder marker present in body.
	ShapeB EpicShape = "path_b"
	// ShapeC — standalone epic declaration present in body.
	ShapeC EpicShape = "path_c"
	// ShapeNone — no valid shape detected; gate rejects.
	ShapeNone EpicShape = ""
)

// placeholderRe mirrors the bash:
//
//	grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder"
var placeholderRe = regexp.MustCompile(`(?i)nightgauge:decompose-later|placeholder.*decompose later|decompose later.*placeholder`)

// standaloneRe mirrors the bash:
//
//	grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues"
var standaloneRe = regexp.MustCompile(`(?i)nightgauge:standalone-epic|standalone epic|intentionally.*no sub-issues`)

// Classify returns the EpicShape for the given epic body and sub-issue count.
// Rules mirror SKILL.md Phase 2.9 exactly:
//   - Path A: subIssueCount > 0 (takes precedence over body markers)
//   - Path B: body matches placeholderRe
//   - Path C: body matches standaloneRe
//   - ShapeNone: none of the above
func Classify(body string, subIssueCount int) EpicShape {
	if subIssueCount > 0 {
		return ShapeA
	}
	if placeholderRe.MatchString(strings.TrimSpace(body)) {
		return ShapeB
	}
	if standaloneRe.MatchString(strings.TrimSpace(body)) {
		return ShapeC
	}
	return ShapeNone
}
