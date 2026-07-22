package heal

import (
	"fmt"
	"strings"
)

// MissingSeedUpdate matches inherited failures that look like a DB seed or
// fixture-data drift after a schema migration. Seed-update fixes require
// knowing the exact schema change and which row(s) to populate — that is not
// deterministically reconstructable from failure logs alone — so the pattern
// opens an informational PR with `pipeline-heal:needs-review` instead of
// generating tree changes itself.
type MissingSeedUpdate struct{}

// NewMissingSeedUpdate constructs the pattern.
func NewMissingSeedUpdate() *MissingSeedUpdate { return &MissingSeedUpdate{} }

// Slug implements HealPattern.
func (p *MissingSeedUpdate) Slug() string { return "missing-seed-update" }

// Description implements HealPattern.
func (p *MissingSeedUpdate) Description() string {
	return "DB seed/fixture data is out of date after a schema migration — opens a needs-review heal PR naming the failing tests and most recent migration."
}

// Matches implements HealPattern. The match requires all failures to be
// classification=inherited AND at least one failure to combine a
// seed/fixture/migration keyword with a db-level path hint. Narrow keyword
// conjunctions keep false positives low.
func (p *MissingSeedUpdate) Matches(failures []BaselineFailure) bool {
	if len(failures) == 0 {
		return false
	}
	keywordHit := false
	for _, f := range failures {
		if f.Classification != "inherited" {
			return false
		}
		body := strings.ToLower(f.Name + " " + f.Details)
		hasKeyword := containsAny(body, "seed", "fixture", "workspace", "migration", "drizzle")
		hasDBPath := containsAny(body, "packages/db/", "drizzle/", "migrations/", "/db/", "supabase/", "schema.")
		if hasKeyword && hasDBPath {
			keywordHit = true
		}
	}
	return keywordHit
}

// GenerateFix returns (HealFix{}, false): the recovery action will open an
// informational PR with `pipeline-heal:needs-review` instead of applying a
// tree change. The PR body produced by the action lists the failing tests
// and a hint to inspect the most recent migration.
func (p *MissingSeedUpdate) GenerateFix(failures []BaselineFailure) (HealFix, bool) {
	body := buildNeedsReviewBody(p, failures,
		"Recent schema migrations likely require a corresponding seed/fixture update on main. "+
			"Inspect the most recent migration in `packages/db/` (or equivalent) and update the seed data so the failing tests pass.")
	return HealFix{
		PRTitle:  fmt.Sprintf("chore(heal): %s — needs review", p.Slug()),
		PRBody:   body,
		PRLabels: []string{"pipeline-heal:needs-review", "pattern:" + p.Slug()},
	}, false
}
