package depgraph

import (
	"strconv"
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

// --- #126: status markers in "## Cross-Repo Dependencies" must be honoured ---

// TestStructuredSectionMarkerGating pins the gating semantics of every status
// marker the "## Cross-Repo Dependencies" section invites authors to use, in
// both the slug (`owner/repo#N`) and URL entry forms.
//
// Before #126 the marker was captured but consulted only for `✅` (to set
// Verified). Every other entry — including one an author wrote specifically to
// say "this is deferred, it does not gate us" — produced a hard scheduler edge.
// Because the epic-blockedBy cascade propagates a parent's blockers to all its
// sub-issues, one such line silently stalled an entire epic sub-tree.
func TestStructuredSectionMarkerGating(t *testing.T) {
	tests := []struct {
		name         string
		entry        string
		wantEdge     bool
		wantVerified bool
	}{
		// --- slug form ---
		{
			name:         "check mark gates and is verified",
			entry:        "- ✅ acme/platform#535 — API endpoint verified",
			wantEdge:     true,
			wantVerified: true,
		},
		{
			name:     "cross mark gates",
			entry:    "- ❌ acme/platform#535 — not yet implemented",
			wantEdge: true,
		},
		{
			name:     "warning gates (documented as watch-this, still a dependency)",
			entry:    "- ⚠️ acme/platform#535 — partial implementation",
			wantEdge: true,
		},
		{
			name:     "pause does not gate",
			entry:    "- ⏸️ acme/platform#535 — store distribution",
			wantEdge: false,
		},
		{
			name:     "unmarked entry gates by default (#132 — no marker is not opt-out)",
			entry:    "- acme/platform#535 — plain entry",
			wantEdge: true,
		},
		{
			name:     "textual deferred token does not gate",
			entry:    "- ⚠️ acme/platform#535 — deferred, tracked for later",
			wantEdge: false,
		},
		{
			name:     "textual not-gating token does not gate",
			entry:    "- ⚠️ acme/platform#535 — informational, not-gating",
			wantEdge: false,
		},

		// --- URL form (extracted via the dep-section URL path) ---
		{
			name:     "check mark URL entry gates",
			entry:    "- ✅ https://github.com/acme/platform/issues/535 — verified",
			wantEdge: true,
		},
		{
			name:     "warning URL entry gates",
			entry:    "- ⚠️ https://github.com/acme/platform/issues/535 — partial",
			wantEdge: true,
		},
		{
			name:     "pause URL entry does not gate",
			entry:    "- ⏸️ https://github.com/acme/platform/issues/535 — store distribution",
			wantEdge: false,
		},
		{
			name:     "textual deferred URL entry does not gate",
			entry:    "- https://github.com/acme/platform/issues/535 — deferred to a later release",
			wantEdge: false,
		},

		// --- non-gating marker wins over an explicit blocked-by phrase ---
		{
			name:     "pause marker overrides a blocked-by phrase on the same line",
			entry:    "- ⏸️ Blocked by acme/platform#535 — deferred, not gating",
			wantEdge: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := "## Goal\n\nSomething.\n\n## Cross-Repo Dependencies\n\n" + tc.entry + "\n\n## Testing Plan\n\nMore.\n"
			refs := ParseCrossRepoRefs(body, nil)

			if !tc.wantEdge {
				if len(refs) != 0 {
					t.Fatalf("entry %q must produce NO dependency edge, got %d: %+v",
						tc.entry, len(refs), refs)
				}
				return
			}

			if len(refs) != 1 {
				t.Fatalf("entry %q must produce exactly 1 dependency edge, got %d: %+v",
					tc.entry, len(refs), refs)
			}
			if refs[0].Repo != "acme/platform" || refs[0].Number != 535 {
				t.Errorf("expected acme/platform#535, got %s#%d", refs[0].Repo, refs[0].Number)
			}
			if refs[0].Verified != tc.wantVerified {
				t.Errorf("Verified = %v, want %v", refs[0].Verified, tc.wantVerified)
			}
		})
	}
}

// TestIsNonGatingLine pins the predicate itself, so the "no edge" outcome for
// ⏸️ is a deliberate classification rather than a side effect of which runes
// happen to be in the entry regex's character class, and so the precedence
// between marker, declaration, and textual token is asserted directly.
func TestIsNonGatingLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"- ⏸️ acme/platform#535 — store distribution", true},
		{"- acme/platform#535 — deferred", true},
		{"- acme/platform#535 — Deferred until Q3", true},
		{"- acme/platform#535 — informational, not-gating", true},
		{"- acme/platform#535 — informational, not gating", true},
		{"- acme/platform#535 — non-gating reference", true},
		{"- ✅ acme/platform#535 — API endpoint verified", false},
		{"- ⚠️ acme/platform#535 — partial implementation", false},
		{"- ❌ acme/platform#535 — not yet implemented", false},
		{"Blocked by acme/platform#535", false},
		{"", false},

		// Precedence: an explicit declaration defeats an incidental textual
		// token, but never the author-placed ⏸️ marker.
		{"- Blocked by acme/platform#491 — needed for the deferred rollout", false},
		{"- Depends on acme/platform#492 — the deferred rollout needs it", false},
		{"Blocked by acme/platform#491 — this work was deferred to beta GTM", false},
		{"- ⏸️ Blocked by acme/platform#535 — deferred, not gating", true},
		{"- ⏸️ Depends on acme/platform#535", true},
	}
	for _, tc := range tests {
		if got := isNonGatingLine(tc.line); got != tc.want {
			t.Errorf("isNonGatingLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestNonGatingPrecedence pins the precedence rule between the three signals
// that can appear on one line: an author-placed marker, an explicit dependency
// declaration, and an incidental textual token.
//
//	⏸️ marker         beats everything (an author placed it deliberately)
//	explicit decl     beats a textual token ("Blocked by X" states intent directly)
//	textual token     applies only where no explicit declaration is present
//
// Suppressing an edge is a *permissive* failure: the scheduler dispatches work
// before its prerequisite, silently, with no operator-visible symptom. That is
// strictly worse than the loud failure #126 fixed, so a word like "deferred"
// appearing incidentally in a sentence must never override an author who
// literally wrote "Blocked by". "deferred" is common vocabulary in these
// repos — the epic behind the original incident says "deferred to beta GTM".
func TestNonGatingPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		entry      string
		wantEdge   bool
		wantNumber int
	}{
		{
			name:       "explicit blocked-by declaration beats an incidental deferred adjective",
			entry:      "- Blocked by acme/platform#491 — needed for the deferred rollout",
			wantEdge:   true,
			wantNumber: 491,
		},
		{
			name:       "explicit depends-on declaration beats an incidental deferred adjective",
			entry:      "- Depends on acme/platform#492 — the deferred rollout needs it",
			wantEdge:   true,
			wantNumber: 492,
		},
		{
			name:     "pause marker beats an explicit declaration",
			entry:    "- ⏸️ Blocked by acme/platform#535 — deferred, not gating",
			wantEdge: false,
		},
		{
			name:     "textual token still suppresses where there is no explicit declaration",
			entry:    "- ⚠️ acme/platform#535 — deferred, tracked for later",
			wantEdge: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := "## Cross-Repo Dependencies\n\n" + tc.entry + "\n"
			refs := ParseCrossRepoRefs(body, nil)

			if !tc.wantEdge {
				if len(refs) != 0 {
					t.Fatalf("entry %q must produce NO dependency edge, got %d: %+v",
						tc.entry, len(refs), refs)
				}
				return
			}

			if len(refs) != 1 {
				t.Fatalf("entry %q must produce exactly 1 dependency edge, got %d: %+v",
					tc.entry, len(refs), refs)
			}
			if refs[0].Repo != "acme/platform" || refs[0].Number != tc.wantNumber {
				t.Errorf("expected acme/platform#%d, got %s#%d",
					tc.wantNumber, refs[0].Repo, refs[0].Number)
			}
		})
	}
}

// TestStructuredSectionMixedMarkers is the live incident from #126 in
// miniature: an epic whose Cross-Repo Dependencies section carries one real
// blocker and one entry it was explicitly rescoped away from. Only the real
// blocker may become an edge.
func TestStructuredSectionMixedMarkers(t *testing.T) {
	body := `## Cross-Repo Dependencies

- ⚠️ acme/platform#209 — store distribution
- ⏸️ acme/mobile#77 — deferred, out of scope for this epic
- ✅ acme/dashboard#12 — shipped
`
	refs := ParseCrossRepoRefs(body, nil)
	got := map[int]bool{}
	for _, r := range refs {
		got[r.Number] = true
	}
	if !got[209] {
		t.Errorf("⚠️ entry #209 must remain a dependency edge, got %+v", refs)
	}
	if !got[12] {
		t.Errorf("✅ entry #12 must remain a dependency edge, got %+v", refs)
	}
	if got[77] {
		t.Errorf("⏸️ entry #77 must NOT produce a dependency edge, got %+v", refs)
	}
}

// TestParseRefsCarrySourceLine verifies that every ref records the body line it
// came from, so a dispatch blocked by body prose can name the prose. Without
// this, diagnosing #126 required reading parser.go.
func TestParseRefsCarrySourceLine(t *testing.T) {
	body := `## Cross-Repo Dependencies

- ⚠️ acme/platform#209 — store distribution
`
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].SourceLine != "- ⚠️ acme/platform#209 — store distribution" {
		t.Errorf("SourceLine = %q, want the originating body line", refs[0].SourceLine)
	}
}

// --- #132: unmarked "## Cross-Repo Dependencies" entries gate by default ---

// TestUnmarkedEntryGates pins the corrected behavior: a bare, unmarked entry
// under "## Cross-Repo Dependencies" is a real blocker to a human author, so
// it must produce a gating edge (Verified false, since no ✅ was written) —
// the opposite of the behavior #126 pinned.
func TestUnmarkedEntryGates(t *testing.T) {
	body := "## Cross-Repo Dependencies\n\n- acme/platform#535 — really blocks us\n"
	refs := ParseCrossRepoRefs(body, nil)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].Repo != "acme/platform" || refs[0].Number != 535 {
		t.Errorf("expected acme/platform#535, got %s#%d", refs[0].Repo, refs[0].Number)
	}
	if refs[0].Verified {
		t.Errorf("Verified = true, want false for an unmarked entry")
	}
}

// TestUnmarkedEntryWithNonGatingTokenDoesNotGate confirms an unmarked entry
// can still opt out of gating via ⏸️ or a textual "deferred"/"not-gating"
// token — isNonGatingLine's precedence is unchanged by the marker-optional
// regex.
func TestUnmarkedEntryWithNonGatingTokenDoesNotGate(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"pause marker", "- ⏸️ acme/platform#535 — store distribution"},
		{"deferred token", "- acme/platform#535 — deferred to a later release"},
		{"not-gating token", "- acme/platform#535 — informational, not-gating"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := "## Cross-Repo Dependencies\n\n" + tc.entry + "\n"
			refs := ParseCrossRepoRefs(body, nil)
			if len(refs) != 0 {
				t.Fatalf("entry %q must produce NO dependency edge, got %d: %+v",
					tc.entry, len(refs), refs)
			}
		})
	}
}

// TestUnmarkedEntryMixedWithMarkedEntries is the #132 sibling of
// TestStructuredSectionMixedMarkers: a section combining unmarked, marked-
// gating, and marked-suppressed entries must produce edges for every gating
// entry (unmarked included) and dedup/order without dropping any.
func TestUnmarkedEntryMixedWithMarkedEntries(t *testing.T) {
	body := `## Cross-Repo Dependencies

- acme/platform#209 — plain entry, really blocks us
- ⚠️ acme/mobile#77 — watch this
- ⏸️ acme/dashboard#12 — deferred, out of scope
- ✅ acme/platform#209 — duplicate, already listed above
`
	refs := ParseCrossRepoRefs(body, nil)
	got := map[int]bool{}
	for _, r := range refs {
		got[r.Number] = true
	}
	if !got[209] {
		t.Errorf("unmarked entry #209 must produce a dependency edge, got %+v", refs)
	}
	if !got[77] {
		t.Errorf("⚠️ entry #77 must remain a dependency edge, got %+v", refs)
	}
	if got[12] {
		t.Errorf("⏸️ entry #12 must NOT produce a dependency edge, got %+v", refs)
	}
	// #209 appears twice (unmarked, then ✅) — dedup keeps only the first.
	count := 0
	for _, r := range refs {
		if r.Number == 209 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected #209 to be deduplicated to a single ref, got %d: %+v", count, refs)
	}
	for _, r := range refs {
		if r.Number == 209 && r.Verified {
			t.Errorf("first-seen #209 ref must keep Verified=false (unmarked), got %+v", r)
		}
	}
}

// TestMarkedEntriesUnaffectedByOptionalMarkerGroup is a regression guard: the
// marker group in reStructuredEntry became optional (`*` instead of `+`) so
// unmarked entries would match, but every previously-passing marked-entry
// case must parse byte-for-byte identically.
func TestMarkedEntriesUnaffectedByOptionalMarkerGroup(t *testing.T) {
	tests := []struct {
		marker       string
		wantVerified bool
	}{
		{"✅", true},
		{"❌", false},
		{"⚠️", false},
	}
	for _, tc := range tests {
		t.Run(tc.marker, func(t *testing.T) {
			body := "## Cross-Repo Dependencies\n\n- " + tc.marker + " acme/platform#535 — description\n"
			refs := ParseCrossRepoRefs(body, nil)
			if len(refs) != 1 {
				t.Fatalf("expected 1 ref, got %d: %+v", len(refs), refs)
			}
			if refs[0].Repo != "acme/platform" || refs[0].Number != 535 {
				t.Errorf("expected acme/platform#535, got %s#%d", refs[0].Repo, refs[0].Number)
			}
			if refs[0].Verified != tc.wantVerified {
				t.Errorf("Verified = %v, want %v", refs[0].Verified, tc.wantVerified)
			}
		})
	}
}

// --- Same-repo declaration forms (#1492) -------------------------------------
//
// Before #1492 the parser required a repo token before every `#`, so the
// cross-repo spelling of a sentence produced a scheduler edge and the
// same-repo spelling of the identical sentence produced nothing. These pin
// the bare forms, the list forms, and — just as important — the negatives:
// a `#N` with no declaration keyword is prose, not a dependency.

const selfRepo = "nightgauge/nightgauge"

func sameRepoNumbers(t *testing.T, body string) []int {
	t.Helper()
	var got []int
	for _, r := range ParseDependencyRefs(body, selfRepo, nil) {
		if r.Repo == selfRepo {
			got = append(got, r.Number)
		}
	}
	return got
}

func TestParseDependencyRefs_BareSameRepoForms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []int
	}{
		{"depends on with colon", "Depends on: #1187", []int{1187}},
		{"depends on without colon", "Depends on #1187", []int{1187}},
		{"depend singular", "This depend on #1187", []int{1187}},
		{"blocked by", "Blocked by #1187", []int{1187}},
		{"blocked by with colon", "Blocked by: #1187", []int{1187}},
		{"case insensitive", "BLOCKED BY #1187", []int{1187}},
		{"comma list", "Depends on: #1187, #1190", []int{1187, 1190}},
		{"list with and", "Depends on #1187, #1190 and #1195", []int{1187, 1190, 1195}},
		{"semicolon list", "Blocked by #1187; #1190", []int{1187, 1190}},
		{"mid-sentence declaration", "The API rewrite depends on #1187 landing first.", []int{1187}},
		{"under a Dependencies header", "## Dependencies\n\n- #1187 — the API rewrite\n", []int{1187}},
		{"under a Blocked by header", "## Blocked by\n\n- #1187\n- #1190\n", []int{1187, 1190}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRepoNumbers(t, tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestParseDependencyRefs_NegativesStayProse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"plain mention", "See #1187 for background."},
		{"closes keyword", "Closes #1187"},
		{"prose with a number", "Rewrote the #1187 handler naming."},
		{"non-gating marker", "⏸️ Depends on #1187 — recorded for context"},
		// The hyphenated "Depends-on" this case originally used as a
		// deliberately-not-a-keyword spelling IS a keyword since #1505, which
		// made the case exercise the declaration-beats-"deferred" precedence
		// pinned positively in the next test rather than the non-gating token
		// it is named for. Restated with prose that carries no declaration.
		{"deferred text", "Deferred for now: see #1187"},
		{"before the keyword on the same line", "Closes #99 — depends on #100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRepoNumbers(t, tc.body)
			for _, n := range got {
				if n == 1187 || n == 99 {
					t.Fatalf("body %q produced same-repo dependency #%d — a reference "+
						"with no dependency declaration in front of it is prose", tc.body, n)
				}
			}
		})
	}
}

// TestParseDependencyRefs_NonGatingDeferredStillDeclares pins the precedence
// rule the cross-repo parser already honours: an explicit declaration beats an
// incidental "deferred" adjective on the same line.
func TestParseDependencyRefs_DeclarationBeatsIncidentalDeferred(t *testing.T) {
	got := sameRepoNumbers(t, "Blocked by #491 — needed for the deferred rollout")
	if len(got) != 1 || got[0] != 491 {
		t.Fatalf("got %v, want [491] — an explicit declaration outranks a stray adjective", got)
	}
}

// TestParseDependencyRefs_QualifiedRefsNotDoubleCounted is the guard against
// the failure this pass could most easily introduce: reading "platform #535"
// as BOTH a cross-repo edge to platform#535 and a same-repo edge to #535,
// which would block on an unrelated issue in the wrong repository.
func TestParseDependencyRefs_QualifiedRefsNotDoubleCounted(t *testing.T) {
	body := "Blocked by platform #535\nDepends on: acme/platform#600\n"
	refs := ParseDependencyRefs(body, selfRepo, nil)
	for _, r := range refs {
		if r.Repo == selfRepo {
			t.Errorf("repo-qualified ref also produced a same-repo edge to #%d (line %q)",
				r.Number, r.SourceLine)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2 (%v)", len(refs), refs)
	}
}

func TestParseDependencyRefs_MixedQualifiedAndBareOnOneLine(t *testing.T) {
	refs := ParseDependencyRefs("Depends on: platform #535 and #1187", selfRepo, nil)
	var sawPlatform, sawSelf bool
	for _, r := range refs {
		switch {
		case r.Repo == "acme/platform" && r.Number == 535:
			sawPlatform = true
		case r.Repo == selfRepo && r.Number == 1187:
			sawSelf = true
		default:
			t.Errorf("unexpected ref %s#%d", r.Repo, r.Number)
		}
	}
	if !sawPlatform || !sawSelf {
		t.Fatalf("want both platform#535 and %s#1187, got %v", selfRepo, refs)
	}
}

func TestParseDependencyRefs_SelfReferenceAndEmptyRepo(t *testing.T) {
	if refs := ParseDependencyRefs("Depends on: #1187", "", nil); len(refs) != 0 {
		t.Errorf("empty selfRepo must degrade to ParseCrossRepoRefs, got %v", refs)
	}
	refs := ParseDependencyRefs("Depends on: #1187", selfRepo, nil)
	if len(refs) != 1 || refs[0].SourceLine != "Depends on: #1187" {
		t.Fatalf("SourceLine must name the responsible prose, got %v", refs)
	}
}

func TestParseDependencyRefs_Deduplicates(t *testing.T) {
	body := "Depends on: #1187\nBlocked by #1187\n"
	if got := sameRepoNumbers(t, body); len(got) != 1 {
		t.Fatalf("same dependency declared twice must yield one ref, got %v", got)
	}
}

// --- #1497: a parent link in a dependency section is not a dependency ---
//
// PR #1495 made every non-empty line under a `## Dependencies` header a
// dependency declaration. Real bodies put the epic membership line inside that
// region, so `Part of #308` became an edge to the parent epic — and an epic
// never closes before its children, which deadlocks the issue and every
// sibling reached through the epic cascade. These pin the regression body
// verbatim, because the shape of the body is the bug.

func TestParseDependencyRefs_ParentLinkInDependencySectionIsNotADep(t *testing.T) {
	// The body from the regression, verbatim. #308 is the parent epic.
	body := `## Dependencies

Blocked by #300.

(Wave 3)

Part of #308
`
	got := sameRepoNumbers(t, body)
	if len(got) != 1 || got[0] != 300 {
		t.Fatalf("got %v, want [300] — only the blocker is a dependency", got)
	}
	for _, r := range ParseDependencyRefs(body, selfRepo, nil) {
		if r.Number == 308 {
			t.Fatalf("parent epic #308 became a dependency edge (source=%q line=%q) — "+
				"an epic never closes before its children, so this deadlocks the issue",
				r.Source, r.SourceLine)
		}
	}
}

func TestParseDependencyRefs_ListedDepSurvivesAParentLink(t *testing.T) {
	body := "## Dependencies\n\n- #101\n\nPart of #5\n"
	got := sameRepoNumbers(t, body)
	if len(got) != 1 || got[0] != 101 {
		t.Fatalf("got %v, want [101] — the list item gates, the parent link does not", got)
	}
}

// A dependency whose DESCRIPTION mentions one of the bookkeeping words is
// still a dependency: the relation has to introduce the reference. Matching
// the word anywhere on the line would drop real edges, which is the same
// "fails toward never dispatching" direction #1497 is about.
func TestParseDependencyRefs_BookkeepingWordInProseStillGates(t *testing.T) {
	body := "## Dependencies\n\n- #535 — needed for the epic rollout\n"
	got := sameRepoNumbers(t, body)
	if len(got) != 1 || got[0] != 535 {
		t.Fatalf("got %v, want [535] — 'epic' as prose must not disarm a real dependency", got)
	}
}

func TestParseDependencyRefs_BookkeepingRelationsInASection(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"part of", "Part of #308"},
		{"lowercase", "part of #308"},
		{"parent", "Parent: #308"},
		{"parent issue", "Parent issue #308"},
		{"epic", "Epic: #308"},
		{"sub-issue of", "Sub-issue of #308"},
		{"subissue of", "Sub issue of #308"},
		{"child of", "Child of #308"},
		{"tracks", "Tracks #308"},
		{"tracked by", "Tracked by #308"},
		{"related", "Related #308"},
		{"related to", "Related to #308"},
		{"see also", "See also #308"},
		{"closes", "Closes #308"},
		{"fixes", "Fixes #308"},
		{"resolves", "Resolves #308"},
		{"list item parent link", "- Part of #308"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRepoNumbers(t, "## Dependencies\n\n"+tc.line+"\n")
			if len(got) != 0 {
				t.Fatalf("%q under a dependency header yielded %v — it names an "+
					"issue for a reason that is not a dependency", tc.line, got)
			}
		})
	}
}

// The Wave parenthetical carries a number that is not an issue reference at
// all; it must not become one, with or without a `#`.
func TestParseDependencyRefs_WaveParentheticalIsNotARef(t *testing.T) {
	for _, line := range []string{"(Wave 3)", "(Wave #3)"} {
		if got := sameRepoNumbers(t, "## Dependencies\n\n"+line+"\n"); len(got) != 0 {
			t.Errorf("%q yielded %v, want none", line, got)
		}
	}
}

// --- Sentence-scoped dependency keywords (#1502) ----------------------------
//
// A "Blocked by" / "Depends on" keyword used to claim every `#N` from the
// keyword to end of line, so a second sentence sharing the line was promoted
// into a gating edge. The scheduler held two ready flutter issues on a #301
// that only appeared in prose ("Reaches its full value with Epic #301"). A
// fragment now ends at the next keyword or at a sentence terminator.

func TestParseDependencyRefs_SentenceScopedKeywords(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []int
	}{
		{
			// The exact epic body line from the scheduler log.
			name: "second sentence on the same line is prose",
			body: "Blocked by Epic #295. Reaches its full value with Epic #301 — authoring an issue\n",
			want: []int{295},
		},
		{
			name: "two declarations on one line yield both",
			body: "Blocked by #5. Depends on #6\n",
			want: []int{5, 6},
		},
		{
			name: "semicolon terminates the declaration",
			body: "Blocked by #5; see also #7\n",
			want: []int{5},
		},
		{
			name: "question mark terminates the declaration",
			body: "Blocked by #5? Nobody knows about #8\n",
			want: []int{5},
		},
		{
			name: "a period inside a token is not a sentence break",
			body: "Depends on v1.2 of #9\n",
			want: []int{9},
		},
		{
			name: "no terminator keeps the whole remainder",
			body: "Blocked by #11, #12 and #13\n",
			want: []int{11, 12, 13},
		},
		{
			name: "trailing period does not drop the last reference",
			body: "Blocked by #14.\n",
			want: []int{14},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRepoNumbers(t, tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// Each keyword keeps its own source label even when two share a line — the
// scheduler prints `edge=<source>` when it holds a dispatch, so a mislabelled
// fragment sends an operator to the wrong sentence.
func TestParseDependencyRefs_PerKeywordSourceOnOneLine(t *testing.T) {
	want := map[int]string{5: "body_text", 6: "depends_on"}
	got := map[int]string{}
	for _, r := range ParseDependencyRefs("Blocked by #5. Depends on #6\n", selfRepo, nil) {
		if r.Repo == selfRepo {
			got[r.Number] = r.Source
		}
	}
	for num, src := range want {
		if got[num] != src {
			t.Errorf("#%d source = %q, want %q", num, got[num], src)
		}
	}
}

// --- Field-name spellings of the dependency keyword (#1505) -----------------
//
// Authors write the board edge's own field name — `blockedBy`, `depends-on`,
// `**Depends on:**` — copied from the project field they are mirroring. Every
// pattern used to accept the prose spelling only, so a body that declared a
// cross-repo blocker outright produced no edge and the issue was dispatched
// over it. One shared keyword alternation now backs all of them.

func TestParseDependencyRefs_KeywordSpellings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []int
	}{
		{"camelCase in a code span", "This is `blockedBy` #12", []int{12}},
		{"camelCase bare", "blockedBy #12", []int{12}},
		{"hyphenated", "blocked-by #12", []int{12}},
		{"underscored", "blocked_by #12", []int{12}},
		{"dependsOn camelCase", "dependsOn #12", []int{12}},
		{"depends-on hyphenated", "depends-on #12", []int{12}},
		{"depends_on underscored", "depends_on #12", []int{12}},
		{"bold with colon", "**Depends on:** #3, #4", []int{3, 4}},
		{"bold camelCase", "**blockedBy:** #7", []int{7}},
		// A code span naming the field with no reference after it is
		// documentation about the field, not a declaration.
		{"keyword with no reference", "The board field is `blockedBy`.", nil},
		// The keyword must end on a word boundary: this is an identifier.
		{"identifier is not a keyword", "blockedBySomething#5 is a symbol", nil},
		{"identifier with a space", "blockedByDefault #5 is a flag", nil},
		// The halves stay paired — no cross-product.
		{"blocked on is not the keyword", "blocked on #5", nil},
		{"depends by is not the keyword", "depends by #5", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRepoNumbers(t, tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The specimen from #1505: a dashboard issue whose "Reassessment correction"
// paragraph declared two cross-repo blockers in one sentence, the second
// behind a comma clause. It produced no edge at all and was dispatched.
func TestParseDependencyRefs_Issue1505Specimen(t *testing.T) {
	const body = "## Reassessment correction\n\n" +
		"This issue is `blockedBy` acme/platform#1253 and, per " +
		"the epic's Wave-1-first rule, acme/platform#1252 — do " +
		"not retry until both close.\n"

	got := map[string]bool{}
	for _, r := range ParseDependencyRefs(body, selfRepo, nil) {
		got[r.Repo+"#"+strconv.Itoa(r.Number)] = true
		if r.Source != "body_text" {
			t.Errorf("%s#%d source = %q, want body_text", r.Repo, r.Number, r.Source)
		}
	}

	want := []string{
		"acme/platform#1253",
		"acme/platform#1252",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing edge %s (got %v)", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want exactly %v", got, want)
	}
}
