package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─── #1660: issue-pickup's compact render profile ──────────────────────────
//
// issue-pickup is the first stage of every run, so if its render cannot fit
// a small-window model nothing downstream runs on one. These tests pin the
// compact profile (skills/nightgauge-issue-pickup/_profiles/compact.md) to
// the issue's Verification list: it fits the ADR-023 share at a 32768-token
// window, keeps every must-survive marker of the full render (the Phase
// 2.5/2.7/2.8/2.9 gates included) and the no-push-to-main rule verbatim, and
// every Read directive it carries is an absolute, existing path under the
// skill root or its _shared sibling.
//
// The helpers below are shared with compact_feature_validate_test.go (#1663):
// both stages ask the same three questions of their compact render.

const compactTestWindow = 32768

// compactReadDirectiveRE finds a backtick-quoted `....md` path immediately
// after the word "read", in either case. compact_test.go's
// readDirectivePathRE only matches a capitalised "Read"; a lower-case
// "read `x.md` now" is just as much an instruction to the model, so a profile
// check that skipped it would leave half the directives unverified.
var compactReadDirectiveRE = regexp.MustCompile("(?i)\\bread `([^`]+\\.md)`")

// renderStagePair renders stage twice from the real skills tree: the full
// render (no profile) and the compact one. It fails when the compact request
// fell back to full, because every assertion after it would then be checking
// the full render against itself.
func renderStagePair(t *testing.T, stage string) (full, compact *Result) {
	t.Helper()
	root := realSkillsRoot(t)
	full = mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}})
	compact = mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}, Profile: ProfileCompact})
	if compact.Profile != ProfileCompact {
		t.Fatalf("stage %q rendered without its compact profile (warnings: %v)", stage, compact.Warnings)
	}
	if len(compact.Warnings) != 0 {
		t.Errorf("unexpected warnings on the %q compact render: %v", stage, compact.Warnings)
	}
	if len(compact.AllowedTools) == 0 || compact.SkillName == "" {
		t.Errorf("%q compact render lost its frontmatter (AllowedTools=%v, SkillName=%q)", stage, compact.AllowedTools, compact.SkillName)
	}
	return full, compact
}

// legacyIssueRefRE matches a whole parenthetical issue-number citation, with
// its leading whitespace, e.g. " (#1234)" or " (#4098, #4135)". Used only to
// NORMALIZE text for comparison, never to rewrite rendered content: a
// compact profile is allowed to drop a citation to an issue above this
// repository's publication-boundary high-water mark (scripts/
// publication-boundary-check.py) while keeping the rule it names, because a
// NEW line citing such an issue fails that check even when the identical,
// unflagged citation already sits in the tracked base file (grandfathered
// only because those lines are not new). Stripping " (#NNNN[, #NNNN...])"
// symmetrically from both sides of a marker/code-block comparison keeps the
// check honest about everything else: only the citation (and the single
// space before it) is allowed to differ.
var legacyIssueRefRE = regexp.MustCompile(`\s*\(#\d+(?:,\s*#\d+)*\)`)

func stripIssueRefs(s string) string {
	return legacyIssueRefRE.ReplaceAllString(s, "")
}

// missingMarkers returns every must-survive marker (CompactionMarkers: phase
// markers, gate/contract/checklist headings, deny rules) the full render has
// and the compact render does not, comparing with stripIssueRefs applied so
// a dropped issue-number citation (see legacyIssueRefRE) is not reported as
// a missing marker.
func missingMarkers(full, compact string) []string {
	have := map[string]bool{}
	for _, m := range CompactionMarkers(compact) {
		have[stripIssueRefs(m)] = true
	}
	var missing []string
	for _, m := range CompactionMarkers(full) {
		if !have[stripIssueRefs(m)] {
			missing = append(missing, m)
		}
	}
	return missing
}

// assertCompactReadDirectives checks every Read directive in a compact render
// is absolute, lives under the skill directory or the sibling _shared
// directory (the two allow-listed roots a stage reads from), and exists.
func assertCompactReadDirectives(t *testing.T, compact *Result) {
	t.Helper()
	skillDir := filepath.Dir(compact.SkillPath)
	sharedDir := filepath.Join(filepath.Dir(skillDir), "_shared")
	matches := compactReadDirectiveRE.FindAllStringSubmatch(compact.Content, -1)
	if len(matches) == 0 {
		t.Fatal("found no Read directives in the compact render; the profile or the extraction regex drifted")
	}
	for _, m := range matches {
		p := m[1]
		if !filepath.IsAbs(p) {
			t.Errorf("Read directive %q is not an absolute path", p)
			continue
		}
		clean := filepath.Clean(p)
		if clean != p {
			t.Errorf("Read directive %q is not a clean path (resolves to %q)", p, clean)
		}
		if !strings.HasPrefix(clean, skillDir+string(filepath.Separator)) &&
			!strings.HasPrefix(clean, sharedDir+string(filepath.Separator)) {
			t.Errorf("Read directive %q is outside the skill root %q and %q", p, skillDir, sharedDir)
		}
		if st, err := os.Stat(clean); err != nil {
			t.Errorf("Read directive %q does not exist: %v", p, err)
		} else if st.IsDir() {
			t.Errorf("Read directive %q names a directory", p)
		}
	}
}

// fencedBlockRE matches one fenced code block, fence line included.
var fencedBlockRE = regexp.MustCompile("(?ms)^```[a-z]*\\n.*?^```$")

// assertProfileCodeBlocksAreVerbatim is the drift guard a hand-trimmed
// profile needs: the profile copies gate shell out of the skill and its
// includes, and a copy that silently diverged from its source would run a
// different gate on small models than on large ones. Every fenced block in
// the profile file must appear byte-for-byte in the base SKILL.md, one of the
// skill's _includes, or a _shared file.
func assertProfileCodeBlocksAreVerbatim(t *testing.T, stage string) {
	t.Helper()
	root := realSkillsRoot(t)
	skillDir := filepath.Join(root, StageSkillDirs[stage])
	profile, err := os.ReadFile(filepath.Join(skillDir, profilesDir, ProfileCompact+".md"))
	if err != nil {
		t.Fatalf("read compact profile: %v", err)
	}
	var sources []string
	for _, pattern := range []string{
		filepath.Join(skillDir, "SKILL.md"),
		filepath.Join(skillDir, "_includes", "*.md"),
		filepath.Join(root, "_shared", "*.md"),
	} {
		files, _ := filepath.Glob(pattern)
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			sources = append(sources, string(data))
		}
	}
	// Normalized with stripIssueRefs (see its doc comment): a compact profile
	// is allowed to drop an inline "#NNNN" issue citation from a copied
	// block — a false-success guard genuinely quoted from the source, minus
	// a number the publication-boundary check rejects on a new line — while
	// every other byte must still match.
	corpus := stripIssueRefs(strings.Join(sources, "\n"))
	blocks := fencedBlockRE.FindAllString(string(profile), -1)
	if len(blocks) == 0 {
		t.Fatal("found no fenced blocks in the compact profile")
	}
	for _, b := range blocks {
		if !strings.Contains(corpus, stripIssueRefs(b)) {
			t.Errorf("compact profile block is not verbatim from SKILL.md, _includes or _shared:\n%s", b)
		}
	}
}

func TestCompactIssuePickup_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "issue-pickup")
	got := fitCompact(t, "issue-pickup", compact.Content)
	if !got.Fits {
		t.Errorf("Fit(issue-pickup compact, %d) = %+v, want Fits=true", compactTestWindow, got)
	}
}

// TestCompactIssuePickup_BudgetCatchesInlinedIncludes proves the fit check
// above is sensitive: appending the text of the skill's own _includes (what
// the profile moved behind Read directives) takes the render back over the
// share, so a profile that re-inlined them would be caught.
func TestCompactIssuePickup_BudgetCatchesInlinedIncludes(t *testing.T) {
	_, compact := renderStagePair(t, "issue-pickup")
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(compact.SkillPath), "_includes", "*.md"))
	if len(files) == 0 {
		t.Fatal("issue-pickup has no _includes to append")
	}
	inflated := compact.Content
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		inflated += "\n" + string(data)
	}
	if got := fitCompact(t, "issue-pickup", inflated); got.Fits {
		t.Errorf("compact + every _includes file still fits (%+v); the budget check is not discriminating", got)
	}
}

func TestCompactIssuePickup_MarkerParity(t *testing.T) {
	full, compact := renderStagePair(t, "issue-pickup")
	for _, m := range missingMarkers(full.Content, compact.Content) {
		t.Errorf("compact render is missing must-survive marker: %s", m)
	}
}

// TestCompactIssuePickup_KeepsGatesAndContracts names the elements #1660
// lists as retained, so a regression reports which one went, not just that
// the marker sets differ.
func TestCompactIssuePickup_KeepsGatesAndContracts(t *testing.T) {
	_, compact := renderStagePair(t, "issue-pickup")
	for _, want := range []string{
		// The no-direct-main rule, verbatim.
		"Never push to main.",
		// The four gate phases, heading and marker.
		"### Phase 2.5: Signal Stage Start",
		`<!-- phase:start name="signal-stage-start" index=2 total=14 stage="issue-pickup" -->`,
		"### Phase 2.7: Size Gate Preflight",
		`size-gate check`,
		"### Phase 2.8: Baseline-CI Dependency Gate",
		`baseline-gate check`,
		"### Phase 2.9: Native blockedBy Dependency Gate",
		`deps-gate check`,
		"signal=deferred",
		// The artifact contract.
		"## Output Contract",
		"This skill outputs `.nightgauge/pipeline/issue-{N}.json`",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

func TestCompactIssuePickup_ReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	_, compact := renderStagePair(t, "issue-pickup")
	assertCompactReadDirectives(t, compact)
}

func TestCompactIssuePickup_CodeBlocksAreVerbatim(t *testing.T) {
	assertProfileCodeBlocksAreVerbatim(t, "issue-pickup")
}

// fitWorstCaseRoot stands in for the checkout root when a compact render is
// measured. The render carries the absolute skill root in its Read
// directives (they must be absolute: a stage runs in the target repository's
// worktree, which has no skills/ directory), so its size grows with the
// checkout path. The budget is therefore measured at a worst case, not at a
// short root: #1662 passed at the CI runner's 39-character root and failed at
// the 96-character `.nightgauge/worktrees/program-*` root the pipeline runs
// feature-dev in. This root makes a 128-character skills root, longer than
// any worktree path the pipeline creates.
const fitWorstCaseRoot = "/Users/operator-long-name/Repositories/organisation-long-name/nightgauge-checkout/.nightgauge/worktrees/program-a1b2c3d4e"

// fitWorstCaseSkillsRootLen is the length of fitWorstCaseRoot + "/skills".
const fitWorstCaseSkillsRootLen = 128

// fitCompactAt is Fit on content with the checkout root replaced by root.
func fitCompactAt(t *testing.T, stage, content, root string) FitResult {
	t.Helper()
	abs, err := filepath.Abs(realSkillsRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return Fit(stage, strings.ReplaceAll(content, filepath.Dir(abs), root), compactTestWindow)
}

// fitCompact is Fit on content at the worst-case checkout root, so the
// budget tests give the same answer in every checkout and no compact profile
// fits only because the checkout it was measured in has a short path.
func fitCompact(t *testing.T, stage, content string) FitResult {
	t.Helper()
	return fitCompactAt(t, stage, content, fitWorstCaseRoot)
}

// compactProfileStages are the stages that ship a compact profile.
var compactProfileStages = []string{
	"issue-pickup", "feature-planning", "feature-dev",
	"feature-validate", "pr-create", "pr-merge",
}

// #1662: every compact profile fits at the worst-case checkout root, and the
// measurement really is taken there: the render carries this checkout's root
// (so the substitution happens rather than silently doing nothing), and the
// substituted skills root is the documented worst-case length.
func TestCompactProfiles_FitAtWorstCaseRoot(t *testing.T) {
	if n := len(fitWorstCaseRoot + "/skills"); n != fitWorstCaseSkillsRootLen {
		t.Fatalf("worst-case skills root is %d characters, want %d", n, fitWorstCaseSkillsRootLen)
	}
	abs, err := filepath.Abs(realSkillsRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range compactProfileStages {
		t.Run(stage, func(t *testing.T) {
			compact := mustRender(t, Options{Stage: stage, SkillsRoots: []string{abs}, Profile: ProfileCompact})
			if compact.Profile != ProfileCompact {
				t.Fatalf("stage %q rendered without its compact profile", stage)
			}
			if !strings.Contains(compact.Content, filepath.Dir(abs)) {
				t.Fatalf("the %q compact render never names the checkout root, so the worst-case substitution measures nothing", stage)
			}
			if got := fitCompact(t, stage, compact.Content); !got.Fits {
				t.Errorf("Fit(%s compact, %d) at a %d-character skills root = %+v, want Fits=true",
					stage, compactTestWindow, fitWorstCaseSkillsRootLen, got)
			}
		})
	}
}
