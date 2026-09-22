package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── #1654: the compact render profile mechanism ───────────────────────────
//
// These tests are the Verification section's own list for issue #1654: a
// stage with no compact profile falls back to full (with a warning); full
// and no-flag render byte-identically; the compact render for a stage that
// HAS a profile never drops a must-survive marker the full render carries;
// every Read directive in a compact render is an absolute, existing path
// under the skill root; and the pr-merge compact render fits the ADR-023
// budget at a 32768-token window while keeping the --admin prohibition and
// the post-merge build-check text verbatim.

// realSkillsRoot (render_test.go) skips rather than fails when skills/ is
// not present, the same guard TestFit_PrMergeGoldenCases already uses.

func TestCompactProfile_NoFlagAndFullAreByteIdentical(t *testing.T) {
	root := realSkillsRoot(t)
	noFlag := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}})
	full := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}, Profile: "full"})
	if noFlag.Content != full.Content {
		t.Fatalf("no-flag render and --profile full render differ; they must be byte-identical")
	}
	if noFlag.Profile != "" || full.Profile != "" {
		t.Errorf("Profile = %q / %q, want empty for both (full is never reported as a profile)", noFlag.Profile, full.Profile)
	}
}

func TestCompactProfile_ComposesForPrMerge(t *testing.T) {
	root := realSkillsRoot(t)
	compact := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}, Profile: ProfileCompact})
	full := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}})

	if compact.Profile != ProfileCompact {
		t.Errorf("Profile = %q, want %q", compact.Profile, ProfileCompact)
	}
	if len(compact.Warnings) != 0 {
		t.Errorf("unexpected warnings on a stage WITH a compact profile: %v", compact.Warnings)
	}
	if len(compact.Content) >= len(full.Content) {
		t.Errorf("compact render (%d bytes) is not smaller than full (%d bytes)", len(compact.Content), len(full.Content))
	}
	// Frontmatter-derived fields must survive a compact render: a compact
	// skeleton file carries no frontmatter of its own (render.go re-reads it
	// off the base SKILL.md), and a regression here silently drops the tool
	// allowlist the headless dispatcher depends on.
	if len(compact.AllowedTools) == 0 {
		t.Error("compact render lost AllowedTools (should come from the base SKILL.md's frontmatter)")
	}
	if compact.SkillName == "" {
		t.Error("compact render lost SkillName")
	}
}

// TestCompactProfile_FallsBackToFullWithWarning is the AC1 fallback: a stage
// with no _profiles/compact.md renders full and says so in --json.
func TestCompactProfile_FallsBackToFullWithWarning(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", "---\nname: x\n---\n\nBody with no compact profile.\n")

	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}, Profile: ProfileCompact})
	if res.Profile != "" {
		t.Errorf("Profile = %q, want empty (fell back to full)", res.Profile)
	}
	if !strings.Contains(res.Content, "Body with no compact profile.") {
		t.Errorf("compact fallback did not render the full base content verbatim: %q", res.Content)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "no compact profile") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want one naming the missing compact profile", res.Warnings)
	}
}

// ─── Marker parity ──────────────────────────────────────────────────────────

// stagesWithCompactProfile lists every (stage, root) this repository ships a
// compact profile for. #1654 built pr-merge; each consumer (#1660-#1664) adds
// its stage here.
func stagesWithCompactProfile(t *testing.T) map[string]string {
	t.Helper()
	root := realSkillsRoot(t)
	return map[string]string{"pr-merge": root, "issue-pickup": root, "feature-validate": root}
}

// TestCompactMarkerParity is the mechanical check AC2 asks for: every
// must-survive element (phase marker, gate/contract/checklist heading, deny
// rule) the full render carries is also present in the compact render — a
// superset check, not hand inspection.
func TestCompactMarkerParity(t *testing.T) {
	for stage, root := range stagesWithCompactProfile(t) {
		t.Run(stage, func(t *testing.T) {
			full := mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}})
			compact := mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}, Profile: ProfileCompact})
			if compact.Profile != ProfileCompact {
				t.Fatalf("stage %q has no compact profile; remove it from stagesWithCompactProfile or add _profiles/compact.md", stage)
			}

			compactSet := make(map[string]bool)
			for _, m := range CompactionMarkers(compact.Content) {
				compactSet[m] = true
			}
			for _, m := range CompactionMarkers(full.Content) {
				if !compactSet[m] {
					t.Errorf("compact render for %q is missing must-survive marker: %s", stage, m)
				}
			}
		})
	}
}

// TestCompactMarkerParity_CatchesADeletion proves the parity check in
// TestCompactMarkerParity is sensitive, not vacuously green: a compact
// profile fixture missing one phase-marker line the full render has is
// reported, exactly as deleting a phase-marker line from the real
// _profiles/compact.md would be caught.
func TestCompactMarkerParity_CatchesADeletion(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", `---
name: x
---

## Input Contract

`+"```"+`bash
printf '<!-- phase:start name="a" index=0 total=2 stage="feature-dev" -->\n'
`+"```"+`

`+"```"+`bash
printf '<!-- phase:start name="b" index=1 total=2 stage="feature-dev" -->\n'
`+"```"+`
`)
	// The compact fixture drops phase marker "b" entirely — the deletion
	// this test proves gets caught.
	write(t, filepath.Join(root, "nightgauge-feature-dev", "_profiles", "compact.md"), `## Input Contract

`+"```"+`bash
printf '<!-- phase:start name="a" index=0 total=2 stage="feature-dev" -->\n'
`+"```"+`
`)

	full := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})
	compact := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}, Profile: ProfileCompact})

	fullMarkers := CompactionMarkers(full.Content)
	compactSet := make(map[string]bool)
	for _, m := range CompactionMarkers(compact.Content) {
		compactSet[m] = true
	}
	var missing []string
	for _, m := range fullMarkers {
		if !compactSet[m] {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		t.Fatal("expected the deleted phase marker to be reported missing; the parity check did not catch it")
	}
	wantMissing := `<!-- phase:start name="b" index=1 total=2 stage="feature-dev" -->`
	if missing[0] != wantMissing {
		t.Errorf("missing = %v, want to include %q", missing, wantMissing)
	}
}

// ─── Read directive shape ───────────────────────────────────────────────────

// readDirectivePathRE finds a backtick-quoted `....md` path immediately
// following the word "Read" — the shape every Read directive in this
// package's skills already uses (render.go's own RewriteSkillRelativePaths
// doc comment, and every existing SKILL.md's "Read `_includes/x.md` now").
var readDirectivePathRE = regexp.MustCompile("Read `([^`]+\\.md)`")

// TestCompactReadDirectivesAreAbsoluteAndExist is AC3, mechanically: every
// Read directive in the pr-merge compact render is an absolute path under
// the resolved skill root and names a file that exists on disk.
func TestCompactReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	root := realSkillsRoot(t)
	compact := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}, Profile: ProfileCompact})
	skillRoot := filepath.Dir(compact.SkillPath)

	paths := readDirectivePathRE.FindAllStringSubmatch(compact.Content, -1)
	if len(paths) == 0 {
		t.Fatal("found no Read directives in the compact render; the extraction regex or the profile file drifted")
	}
	for _, m := range paths {
		p := m[1]
		if !filepath.IsAbs(p) {
			t.Errorf("Read directive %q is not an absolute path", p)
			continue
		}
		if !strings.HasPrefix(p, skillRoot) && !strings.HasPrefix(p, filepath.Dir(skillRoot)) {
			t.Errorf("Read directive %q is not under the skill root %q (or its _shared sibling)", p, skillRoot)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Read directive %q does not exist: %v", p, err)
		}
	}
}

// TestCompactReadDirective_RelativeTraversalIsNotRewrittenAbsolute is the
// negative case AC3 names: a fixture profile referencing `../../x.md` is
// never rewritten into a safe absolute path (RewriteSkillRelativePaths only
// rewrites the known skill-relative forms), so a validator checking
// filepath.IsAbs catches it rather than silently trusting it.
func TestCompactReadDirective_RelativeTraversalIsNotRewrittenAbsolute(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", "---\nname: x\n---\n\nBody.\n")
	write(t, filepath.Join(root, "nightgauge-feature-dev", "_profiles", "compact.md"),
		"Read `../../x.md` now and follow its instructions.\n")

	compact := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}, Profile: ProfileCompact})
	for _, m := range readDirectivePathRE.FindAllStringSubmatch(compact.Content, -1) {
		if filepath.IsAbs(m[1]) {
			t.Fatalf("expected the ../../ traversal to survive un-rewritten (not silently made absolute), got %q", m[1])
		}
	}
}

// ─── pr-merge-specific budget and deny-rule guarantees (AC4) ───────────────

func TestCompactPrMerge_FitsBudgetAndKeepsDenyRules(t *testing.T) {
	root := realSkillsRoot(t)
	compact := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}, Profile: ProfileCompact})

	got := Fit("pr-merge", compact.Content, 32768)
	if !got.Fits {
		t.Errorf("Fit(pr-merge compact, 32768) = %+v, want Fits=true", got)
	}

	for _, want := range []string{
		"NEVER pass `--admin`",
		"Post-Merge Build Verification",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing verbatim text %q", want)
		}
	}
}

// TestDecideProfile_CompactWhenAvailable is the budget-decision half of AC5:
// a full render over budget with a compact profile available decides
// "compact"; with no profile it decides "refuse".
func TestDecideProfile_CompactWhenAvailable(t *testing.T) {
	root := realSkillsRoot(t)
	full := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}})
	compact := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}, Profile: ProfileCompact})

	decision, fit := DecideProfile("pr-merge", full.Content, 32768, true, compact.Content)
	if decision != DecisionCompact {
		t.Errorf("DecideProfile with a compact profile available = %v, want %v", decision, DecisionCompact)
	}
	if !fit.Fits {
		t.Errorf("DecideProfile's returned FitResult should be the COMPACT verdict (fits), got %+v", fit)
	}

	decision2, _ := DecideProfile("pr-merge", full.Content, 32768, false, "")
	if decision2 != DecisionRefuse {
		t.Errorf("DecideProfile with no compact profile = %v, want %v", decision2, DecisionRefuse)
	}

	// A render that already fits never even looks at the compact content.
	decision3, _ := DecideProfile("pr-merge", full.Content, 262144, true, "")
	if decision3 != DecisionFits {
		t.Errorf("DecideProfile for a render that already fits = %v, want %v", decision3, DecisionFits)
	}
}
