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

// missingMarkers returns every must-survive marker (CompactionMarkers: phase
// markers, gate/contract/checklist headings, deny rules) the full render has
// and the compact render does not.
func missingMarkers(full, compact string) []string {
	have := map[string]bool{}
	for _, m := range CompactionMarkers(compact) {
		have[m] = true
	}
	var missing []string
	for _, m := range CompactionMarkers(full) {
		if !have[m] {
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
	corpus := strings.Join(sources, "\n")
	blocks := fencedBlockRE.FindAllString(string(profile), -1)
	if len(blocks) == 0 {
		t.Fatal("found no fenced blocks in the compact profile")
	}
	for _, b := range blocks {
		if !strings.Contains(corpus, b) {
			t.Errorf("compact profile block is not verbatim from SKILL.md, _includes or _shared:\n%s", b)
		}
	}
}

func TestCompactIssuePickup_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "issue-pickup")
	got := Fit("issue-pickup", compact.Content, compactTestWindow)
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
	if got := Fit("issue-pickup", inflated, compactTestWindow); got.Fits {
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
