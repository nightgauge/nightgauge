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
