package heal

import (
	"fmt"
	"strings"
)

// RemovedExport matches inherited failures that look like a TypeScript/JS
// import resolves but the named export is missing or the module cannot be
// found. The fix requires knowing the original API surface, which is not
// deterministically reconstructable from failure logs — so this pattern
// always opens an informational PR with `pipeline-heal:needs-review`.
type RemovedExport struct{}

// NewRemovedExport constructs the pattern.
func NewRemovedExport() *RemovedExport { return &RemovedExport{} }

// Slug implements HealPattern.
func (p *RemovedExport) Slug() string { return "removed-export" }

// Description implements HealPattern.
func (p *RemovedExport) Description() string {
	return "Test or build fails because a monorepo package export was removed but still imported — opens a needs-review heal PR naming the importer and missing symbol."
}

// Matches implements HealPattern. All failures must be inherited AND at
// least one must combine a TS/JS export-missing phrasing with a known
// monorepo package import (`@nightgauge/`, `packages/`).
func (p *RemovedExport) Matches(failures []BaselineFailure) bool {
	if len(failures) == 0 {
		return false
	}
	for _, f := range failures {
		if f.Classification != "inherited" {
			return false
		}
	}
	for _, f := range failures {
		body := strings.ToLower(f.Details)
		hasExportPhrase := containsAny(body,
			"has no exported member",
			"is not exported from",
			"cannot find name",
			"module not found",
			"has no exports matching",
			"is not a function",
		)
		if !hasExportPhrase {
			continue
		}
		if containsAny(body, "@nightgauge/", "packages/", "@acme/") {
			return true
		}
	}
	return false
}

// GenerateFix returns (HealFix{}, false): adding back a removed export
// requires understanding the original surface area, so this pattern always
// surfaces to human review.
func (p *RemovedExport) GenerateFix(failures []BaselineFailure) (HealFix, bool) {
	body := buildNeedsReviewBody(p, failures,
		"An export referenced by a still-published consumer appears to have been removed from a monorepo package. "+
			"Re-add the removed export (or update the consumers in a coordinated change) — adding it back from failure logs alone is not deterministic.")
	return HealFix{
		PRTitle:  fmt.Sprintf("chore(heal): %s — needs review", p.Slug()),
		PRBody:   body,
		PRLabels: []string{"pipeline-heal:needs-review", "pattern:" + p.Slug()},
	}, false
}
