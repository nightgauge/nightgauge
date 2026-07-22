package depgraph

import (
	"testing"
)

func TestParseBlockedBy(t *testing.T) {
	body := "This issue is Blocked by platform #535 and needs attention."
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Repo != "acme/platform" {
		t.Errorf("expected platform repo, got %q", refs[0].Repo)
	}
	if refs[0].Number != 535 {
		t.Errorf("expected #535, got #%d", refs[0].Number)
	}
	if refs[0].Source != "body_text" {
		t.Errorf("expected source body_text, got %q", refs[0].Source)
	}
}

func TestParseBlockedByFullRepoName(t *testing.T) {
	body := "blocked by acme/mobile#127"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Repo != "acme/mobile" {
		t.Errorf("expected acme/mobile repo, got %q", refs[0].Repo)
	}
	if refs[0].Number != 127 {
		t.Errorf("expected #127, got #%d", refs[0].Number)
	}
}

func TestParseBlockedByCaseInsensitive(t *testing.T) {
	body := "BLOCKED BY flutter #99"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Number != 99 {
		t.Errorf("expected #99, got #%d", refs[0].Number)
	}
}

func TestParseDependsOn(t *testing.T) {
	body := "Depends on: flutter #127"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Repo != "acme/mobile" {
		t.Errorf("expected flutter repo, got %q", refs[0].Repo)
	}
	if refs[0].Number != 127 {
		t.Errorf("expected #127, got #%d", refs[0].Number)
	}
	if refs[0].Source != "depends_on" {
		t.Errorf("expected source depends_on, got %q", refs[0].Source)
	}
}

func TestParseDependsOnMultiple(t *testing.T) {
	body := `Depends on flutter #127
Depends on angular #152`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Number != 127 {
		t.Errorf("expected first ref #127, got #%d", refs[0].Number)
	}
	if refs[1].Number != 152 {
		t.Errorf("expected second ref #152, got #%d", refs[1].Number)
	}
}

func TestParseDependsOnWithoutColon(t *testing.T) {
	body := "Depends on platform #42"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Number != 42 {
		t.Errorf("expected #42, got #%d", refs[0].Number)
	}
}

func TestParseStructuredSection(t *testing.T) {
	body := `## Implementation Plan

Some content here.

## Cross-Repo Dependencies

- ✅ platform #535 — API endpoint ready
- ❌ flutter #127 — Mobile UI not started
- ⚠️ angular #152 — Partially implemented

## Testing Plan

More content.
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}

	// Check first ref (platform)
	found535 := false
	found127 := false
	found152 := false
	for _, ref := range refs {
		switch ref.Number {
		case 535:
			found535 = true
			if ref.Source != "structured_section" {
				t.Errorf("#535 source should be structured_section, got %q", ref.Source)
			}
			if !ref.Verified {
				t.Error("#535 should be verified (✅)")
			}
		case 127:
			found127 = true
			if ref.Verified {
				t.Error("#127 should not be verified (❌)")
			}
		case 152:
			found152 = true
			if ref.Verified {
				t.Error("#152 should not be verified (⚠️)")
			}
		}
	}

	if !found535 || !found127 || !found152 {
		t.Error("not all expected refs found")
	}
}

func TestParseEmptyBody(t *testing.T) {
	refs := ParseCrossRepoRefs("", nil)
	if refs != nil {
		t.Errorf("expected nil for empty body, got %v", refs)
	}
}

func TestParseDeduplicate(t *testing.T) {
	// Same ref via blocked-by AND depends-on should only appear once
	body := `Blocked by platform #100
Depends on platform #100`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Errorf("expected 1 ref (deduped), got %d", len(refs))
	}
}

func TestParseCustomAliases(t *testing.T) {
	aliases := map[string]string{
		"api": "MyOrg/api-service",
	}
	body := "Blocked by api #42"
	refs := ParseCrossRepoRefs(body, aliases)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Repo != "MyOrg/api-service" {
		t.Errorf("expected MyOrg/api-service, got %q", refs[0].Repo)
	}
}

func TestParseUnknownAlias(t *testing.T) {
	body := "Blocked by unknown-repo #42"
	refs := ParseCrossRepoRefs(body, map[string]string{})
	if len(refs) != 0 {
		t.Errorf("unknown alias should produce no refs, got %d", len(refs))
	}
}

func TestResolveAliasExactMatch(t *testing.T) {
	aliases := map[string]string{"platform": "acme/platform"}
	got := resolveAlias("platform", aliases)
	if got != "acme/platform" {
		t.Errorf("exact match failed: %q", got)
	}
}

func TestResolveAliasCaseInsensitive(t *testing.T) {
	aliases := map[string]string{"Platform": "acme/platform"}
	got := resolveAlias("platform", aliases)
	if got != "acme/platform" {
		t.Errorf("case insensitive match failed: %q", got)
	}
}

func TestResolveAliasOwnerSlashRepo(t *testing.T) {
	// Already a full name — should be returned as-is
	got := resolveAlias("SomeOrg/some-repo", map[string]string{})
	if got != "SomeOrg/some-repo" {
		t.Errorf("owner/repo pass-through failed: %q", got)
	}
}

func TestResolveAliasUnknown(t *testing.T) {
	got := resolveAlias("nonexistent", map[string]string{})
	if got != "" {
		t.Errorf("unknown alias should return empty, got %q", got)
	}
}

func TestParseMultipleBlockedBy(t *testing.T) {
	body := `Blocked by platform #100
Blocked by flutter #200
Blocked by angular #300`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 3 {
		t.Errorf("expected 3 refs, got %d", len(refs))
	}
}

func TestParseStructuredSectionNoHeader(t *testing.T) {
	// Without the cross-repo section header, structured entries should NOT match
	body := `- ✅ platform #535 — stuff
- ❌ flutter #127`
	refs := ParseCrossRepoRefs(body, nil)
	// These should only be picked up if there's a header; without it they
	// shouldn't match via the structured section parser. They may or may not
	// match other patterns (they don't match blocked-by or depends-on).
	for _, ref := range refs {
		if ref.Source == "structured_section" {
			t.Error("structured_section match should only occur under the section header")
		}
	}
}

func TestParseNoRefs(t *testing.T) {
	body := "This is a normal issue body with no cross-repo references."
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestParseSameRepoRef(t *testing.T) {
	// "Blocked by nightgauge #42" should resolve to the core repo
	body := "Blocked by nightgauge #42"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Repo != "nightgauge/nightgauge" {
		t.Errorf("expected core repo, got %q", refs[0].Repo)
	}
}

func TestParseDependSingular(t *testing.T) {
	body := "Depend on platform #77"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Number != 77 {
		t.Errorf("expected #77, got #%d", refs[0].Number)
	}
}

// --- URL section-scoping tests (#3635) -------------------------------------
//
// These tests pin the fix for the silent-blocker defect where any GitHub or
// GitLab issue URL anywhere in the body — including descriptive prose like
// "After [#3261](https://github.com/.../issues/3261) lands…" — was promoted
// into a hard dependency edge. URL extraction must be scoped to
// dependency-declaration contexts: under a ## Blocked by / ## Depends on /
// ## Dependencies / ## Cross-Repo Dependencies header, or on the same line
// as a "blocked by" / "depends on" textual marker.

func TestParseURLInGoalSectionIgnored(t *testing.T) {
	// URL in Goal prose must NOT be extracted — it's descriptive context,
	// not a dependency declaration. This is the exact failure mode that
	// blocked #3269 and #3270 from autonomous dispatch.
	body := `## Goal

After [#3261](https://github.com/nightgauge/nightgauge/issues/3261) lands,
the deterministic-first stages will ship.

## Plan

1. Do the thing.
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("URL in Goal prose must not produce refs, got %d: %v", len(refs), refs)
	}
}

func TestParseURLInBlockedBySectionExtracted(t *testing.T) {
	body := `## Goal

Some goal text.

## Blocked by

- [#3264](https://github.com/nightgauge/nightgauge/issues/3264) (pr-merge)
- [#3265](https://github.com/nightgauge/nightgauge/issues/3265) (pr-create)
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs from Blocked by section, got %d: %v", len(refs), refs)
	}
	nums := map[int]bool{}
	for _, r := range refs {
		nums[r.Number] = true
		if r.Source != "body_text" {
			t.Errorf("URL ref #%d should have source body_text, got %q", r.Number, r.Source)
		}
		if r.SourceURL == "" {
			t.Errorf("URL ref #%d should preserve SourceURL", r.Number)
		}
	}
	if !nums[3264] || !nums[3265] {
		t.Errorf("expected #3264 and #3265, got %v", nums)
	}
}

func TestParseURLInDependsOnSectionExtracted(t *testing.T) {
	body := `## Depends on

- [Platform API #99](https://github.com/acme/platform/issues/99)
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref from Depends on section, got %d", len(refs))
	}
	if refs[0].Number != 99 || refs[0].Repo != "acme/platform" {
		t.Errorf("expected platform #99, got %s#%d", refs[0].Repo, refs[0].Number)
	}
}

func TestParseURLInDependenciesSectionExtracted(t *testing.T) {
	body := `## Dependencies

- https://github.com/nightgauge/nightgauge/issues/42
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref from Dependencies section, got %d", len(refs))
	}
	if refs[0].Number != 42 {
		t.Errorf("expected #42, got #%d", refs[0].Number)
	}
}

func TestParseURLOnBlockedByMarkerLine(t *testing.T) {
	// Even outside a section header, a URL on a "blocked by" line is a dep.
	body := `Some prose here.

Blocked by https://github.com/nightgauge/nightgauge/issues/100 — see comments.

More prose.`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref from blocked-by marker line, got %d: %v", len(refs), refs)
	}
	if refs[0].Number != 100 {
		t.Errorf("expected #100, got #%d", refs[0].Number)
	}
}

func TestParseURLInPlanSectionIgnored(t *testing.T) {
	body := `## Plan

1. After [#3264](https://github.com/nightgauge/nightgauge/issues/3264) and
   [#3265](https://github.com/nightgauge/nightgauge/issues/3265) ship,
   re-baseline the budget caps.
2. Update [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("URLs in Plan prose must not produce refs, got %d: %v", len(refs), refs)
	}
}

func TestParseURLInAcceptanceCriteriaIgnored(t *testing.T) {
	body := `## Acceptance criteria

- [ ] Tracked by [#999](https://github.com/nightgauge/nightgauge/issues/999).
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("URLs in Acceptance criteria must not produce refs, got %d: %v", len(refs), refs)
	}
}

func TestParseExact3269BodyPattern(t *testing.T) {
	// Regression for the live failure that motivated #3635. The Goal section
	// URL-references the parent epic #3261 (OPEN, Status=Ready). Before the
	// fix this produced a spurious dep edge that blocked autonomous dispatch.
	// After the fix only the Blocked by section deps appear.
	body := `## Goal

After [#3261](https://github.com/nightgauge/nightgauge/issues/3261) lands,
atomic stages shift to deterministic-default.

Out of scope from [#3261](https://github.com/nightgauge/nightgauge/issues/3261)'s epic body.

## Plan

1. After [#3264](https://github.com/nightgauge/nightgauge/issues/3264) and
   [#3265](https://github.com/nightgauge/nightgauge/issues/3265) ship, gather samples.

## Acceptance criteria

- [ ] New caps committed.

## Blocked by

- [#3264](https://github.com/nightgauge/nightgauge/issues/3264) (pr-merge)
- [#3265](https://github.com/nightgauge/nightgauge/issues/3265) (pr-create)
- [#3267](https://github.com/nightgauge/nightgauge/issues/3267) (gates everywhere)
`
	refs := ParseCrossRepoRefs(body, nil)
	gotNums := map[int]bool{}
	for _, r := range refs {
		gotNums[r.Number] = true
	}
	// Must include the Blocked by deps.
	for _, want := range []int{3264, 3265, 3267} {
		if !gotNums[want] {
			t.Errorf("expected dep #%d from Blocked by section, missing from %v", want, gotNums)
		}
	}
	// Must NOT include #3261 — it appears only in prose (Goal section).
	if gotNums[3261] {
		t.Errorf("regression: prose URL ref to #3261 was extracted as dep — #3635 bug returned. refs=%v", refs)
	}
}

func TestParseURLNoDepContextProducesNoRefs(t *testing.T) {
	// Body with URLs but no dep section and no blocked-by/depends-on marker:
	// URLs are pure references, not deps.
	body := `See https://github.com/nightgauge/nightgauge/issues/100 for context.
Related: https://github.com/acme/platform/issues/55`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("URLs outside dep context must not produce refs, got %d: %v", len(refs), refs)
	}
}

func TestParseURLGitLabInDepSection(t *testing.T) {
	body := `## Blocked by

- https://gitlab.com/myorg/myproject/-/issues/42
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 GitLab URL ref from Blocked by, got %d", len(refs))
	}
	if refs[0].Number != 42 || refs[0].Repo != "myorg/myproject" {
		t.Errorf("expected myorg/myproject#42, got %s#%d", refs[0].Repo, refs[0].Number)
	}
}

func TestParseURLGitLabInProseIgnored(t *testing.T) {
	body := `## Goal

See https://gitlab.com/myorg/myproject/-/issues/42 for context.`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 0 {
		t.Errorf("GitLab URL in prose must not produce refs, got %d: %v", len(refs), refs)
	}
}

func TestParseURLAndSlugDedup(t *testing.T) {
	// A dep referenced via both URL (in Blocked by section) and slug form
	// (via "Blocked by" prefix) must be counted exactly once.
	body := `Blocked by platform #99

## Blocked by

- https://github.com/acme/platform/issues/99
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Errorf("URL + slug form of same ref must dedup to 1, got %d: %v", len(refs), refs)
	}
}

func TestParseDepSectionSubheader(t *testing.T) {
	// ### subheader (level 3) should also count as a dep section.
	body := `## Implementation

### Blocked by

- [#42](https://github.com/nightgauge/nightgauge/issues/42)
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref from ### Blocked by, got %d", len(refs))
	}
	if refs[0].Number != 42 {
		t.Errorf("expected #42, got #%d", refs[0].Number)
	}
}

func TestParseMultipleDepSections(t *testing.T) {
	// Both ## Blocked by and ## Depends on present — URLs in both must extract.
	body := `## Goal

[#1](https://github.com/nightgauge/nightgauge/issues/1) is context only.

## Blocked by

- [#2](https://github.com/nightgauge/nightgauge/issues/2)

## Depends on

- [#3](https://github.com/nightgauge/nightgauge/issues/3)
`
	refs := ParseCrossRepoRefs(body, nil)
	gotNums := map[int]bool{}
	for _, r := range refs {
		gotNums[r.Number] = true
	}
	if !gotNums[2] || !gotNums[3] {
		t.Errorf("expected #2 and #3 from dep sections, got %v", gotNums)
	}
	if gotNums[1] {
		t.Errorf("#1 was in Goal prose, must not be extracted. got %v", gotNums)
	}
}

func TestExtractDepContextEmpty(t *testing.T) {
	if got := extractDepContext(""); got != "" {
		t.Errorf("empty body should return empty context, got %q", got)
	}
}

func TestExtractDepContextNoSections(t *testing.T) {
	got := extractDepContext("Just prose with no dep markers and no headers.")
	if got != "" {
		t.Errorf("body with no dep context should return empty, got %q", got)
	}
}
