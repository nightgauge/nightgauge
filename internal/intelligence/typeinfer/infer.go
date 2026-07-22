// Package typeinfer — Infer consolidates the keyword-based `type:*`
// classification logic previously duplicated in shell across two pipeline
// skills:
//   - skills/nightgauge-backlog-preflight/SKILL.md Phase 4
//   - skills/nightgauge-issue-refine/SKILL.md Phase 2.1
//
// The pure function Infer(InferInput) Result mirrors the union of the two
// shell rule sets so observed classifications are preserved 1:1. The package
// follows the routing/ layout (pure function + Cobra wrapper in
// cmd/nightgauge/main.go) established by audit row B4.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B12.
package typeinfer

import "strings"

// InferInput is the issue metadata required to classify a `type:*` label.
// Title and Body are matched case-insensitively as token contains-checks.
// Labels are scanned for an existing `type:*` slug, which always wins.
type InferInput struct {
	Title  string
	Body   string
	Labels []string
}

// Result is the inference verdict.
//
// Type is one of: `type:bug`, `type:feature`, `type:docs`, `type:refactor`,
// `type:chore`. Source is one of: `label` (an explicit `type:*` label was
// present), `keyword` (matched a token in title or body), `default`
// (fallback to `type:feature` when nothing matched).
type Result struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

// Source values.
const (
	SourceLabel   = "label"
	SourceKeyword = "keyword"
	SourceDefault = "default"
)

// validTypeLabels enumerates the labels Infer is willing to return as a
// `source: label` verdict. Other `type:*` labels (e.g. `type:epic`,
// `type:spike`, `type:verification`) are intentionally ignored — this verb
// is the keyword-classification helper, not a generic label reader.
var validTypeLabels = map[string]bool{
	"type:bug":      true,
	"type:feature":  true,
	"type:docs":     true,
	"type:refactor": true,
	"type:chore":    true,
}

// keywordRule pairs a target type with the lowercased tokens that select it.
// Order matters: the first matching rule wins. Bug keywords come first
// because both consumer skills check them before any other class.
var keywordRules = []struct {
	tokens []string
	typ    string
}{
	{
		tokens: []string{
			"bug", "error", "exception", "crash", "broken",
			"fail", "wrong", "regression", "stack trace", "fix",
		},
		typ: "type:bug",
	},
	{
		tokens: []string{"doc", "readme", "guide"},
		typ:    "type:docs",
	},
	{
		tokens: []string{"refactor", "clean", "simplify"},
		typ:    "type:refactor",
	},
	{
		tokens: []string{"chore", "maintain", "update dep"},
		typ:    "type:chore",
	},
}

// Infer returns the deterministic Result for an issue. Source priority:
// existing `type:*` label > body keywords > title keywords > default.
// The function is pure: identical inputs always produce identical outputs.
func Infer(in InferInput) Result {
	if labeled := matchTypeLabel(in.Labels); labeled != "" {
		return Result{Type: labeled, Source: SourceLabel}
	}

	if typ := matchKeywords(in.Body); typ != "" {
		return Result{Type: typ, Source: SourceKeyword}
	}
	if typ := matchKeywords(in.Title); typ != "" {
		return Result{Type: typ, Source: SourceKeyword}
	}

	return Result{Type: "type:feature", Source: SourceDefault}
}

// matchTypeLabel returns the first valid `type:*` slug found in labels, or "".
func matchTypeLabel(labels []string) string {
	for _, l := range labels {
		lower := strings.ToLower(strings.TrimSpace(l))
		if validTypeLabels[lower] {
			return lower
		}
	}
	return ""
}

// matchKeywords returns the first rule's type whose tokens are present in
// the lowercased text. Returns "" when no rule matches.
func matchKeywords(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, rule := range keywordRules {
		for _, token := range rule.tokens {
			if strings.Contains(lower, token) {
				return rule.typ
			}
		}
	}
	return ""
}
