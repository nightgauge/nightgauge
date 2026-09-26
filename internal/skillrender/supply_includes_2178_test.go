package skillrender

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSupplyPhaseIncludes2178(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "nightgauge-feature-planning")
	write(t, filepath.Join(skillDir, "_includes", "a.md"), "# A\nstep a\n")
	write(t, filepath.Join(skillDir, "_includes", "big.md"), strings.Repeat("b", 200)+"\n")
	write(t, filepath.Join(skillDir, "_includes", "c.md"), "# C\nstep c\n")
	write(t, filepath.Join(skillDir, "_includes", "lazy.md"), "# LAZY\n")
	body := "## Phase 1\n\n> **Read `_includes/a.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**\n\n" +
		"## Phase 2\n\n> **Read `_includes/big.md` (same directory as this SKILL.md) now and follow it.**\n\n" +
		"## Phase 3\n\n> **Read `_includes/c.md` (same directory as this\n> SKILL.md) now and follow it.**\n\n" +
		"## Phase 4\n\n> **Read `_includes/a.md` now and follow it again.**\n\n" +
		"Config detail: Read `_includes/lazy.md` when needed.\n\n" +
		"> **Read `_includes/missing.md` (same directory as this SKILL.md) now.**\n"

	got, supplied := SupplyPhaseIncludes(body, skillDir, 100)
	if strings.Join(supplied, ",") != "a.md,c.md" {
		t.Fatalf("supplied = %v, want [a.md c.md] (big.md over budget, lazy.md on demand, missing.md absent)", supplied)
	}
	for _, want := range []string{
		"Follow `_includes/a.md` (supplied in full",
		"Follow `_includes/c.md` (supplied in full",
		"Read `_includes/big.md` (same directory as this SKILL.md) now",
		"Read `_includes/lazy.md` when needed",
		"Read `_includes/missing.md` (same directory as this SKILL.md) now",
		SuppliedIncludesHeading,
		"### `_includes/a.md`\n\n# A\nstep a\n",
		"### `_includes/c.md`\n\n# C\nstep c\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q", want)
		}
	}
	if strings.Count(got, "step a") != 1 {
		t.Errorf("a.md supplied %d times, want once", strings.Count(got, "step a"))
	}
	if strings.Contains(got, "# LAZY") || strings.Contains(got, strings.Repeat("b", 200)) {
		t.Error("an on-demand or over-budget include was supplied")
	}

	if again, _ := SupplyPhaseIncludes(body, skillDir, 100); again != got {
		t.Error("not deterministic")
	}
	if off, s := SupplyPhaseIncludes(body, skillDir, -1); off != body || s != nil {
		t.Error("a negative budget changed the body")
	}
}

// phaseReadNowRE is a mandatory read of a skill-local include left in a
// rendered prompt, in absolute (rewritten) form.
var phaseReadNowRE = regexp.MustCompile("Read `[^`]*/_includes/[^`]+\\.md`([\\s>]+\\(same[\\s>]+directory[\\s>]+as[\\s>]+this[\\s>]+SKILL\\.md\\))?[\\s>]+now")

// planningPromptBudget is the stated budget for a rendered feature-planning
// stage prompt (#2178): the skill, its expanded shared files and every
// include a phase directs it to follow. At ~2.3 characters per token on a
// Qwen tokenizer this is under 50k tokens, the whole of the planning
// prompt, where #1659 leg 1 run 11 spent 51k characters on the prompt plus
// 52k more on reading the same includes back as tool results.
const planningPromptBudget = 112 * 1024

// TestPlanningPromptSuppliesItsIncludesWithinBudget2178 renders the real
// feature-planning skill: every include a phase says to read now is
// supplied, no such read directive is left, and the prompt stays within the
// stated budget.
func TestPlanningPromptSuppliesItsIncludesWithinBudget2178(t *testing.T) {
	root := realSkillsRoot(t)
	res := mustRender(t, Options{Stage: "feature-planning", SkillsRoots: []string{root}})
	want := []string{"feedback-and-context.md", "pattern-and-docs.md", "knowledge-recall.md", "plan-and-enrichment.md"}
	if strings.Join(res.SuppliedIncludes, ",") != strings.Join(want, ",") {
		t.Errorf("supplied = %v, want %v", res.SuppliedIncludes, want)
	}
	if m := phaseReadNowRE.FindString(res.Content); m != "" {
		t.Errorf("a phase still directs a read of a supplied include: %q", m)
	}
	if len(res.Content) > planningPromptBudget {
		t.Errorf("feature-planning prompt is %d bytes, over the %d budget", len(res.Content), planningPromptBudget)
	}
	t.Logf("feature-planning prompt: %d bytes, includes supplied: %v", len(res.Content), res.SuppliedIncludes)

	compact := mustRender(t, Options{Stage: "feature-planning", SkillsRoots: []string{root}, Profile: ProfileCompact})
	if len(compact.SuppliedIncludes) != 0 {
		t.Errorf("the compact profile supplied %v; it keeps its includes as reads", compact.SuppliedIncludes)
	}
}

// TestEveryStageSuppliesWithinDefaultBudget2178 bounds what the default
// budget adds to each pipeline stage's prompt.
func TestEveryStageSuppliesWithinDefaultBudget2178(t *testing.T) {
	root := realSkillsRoot(t)
	for _, stage := range []string{"issue-pickup", "feature-planning", "feature-dev", "feature-validate", "pr-create", "pr-merge"} {
		base := mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}, IncludeBudget: -1})
		full := mustRender(t, Options{Stage: stage, SkillsRoots: []string{root}})
		if len(full.SuppliedIncludes) == 0 {
			t.Errorf("%s: no include supplied", stage)
		}
		if added := len(full.Content) - len(base.Content); added > DefaultIncludeBudget+4096 {
			t.Errorf("%s: supplying includes added %d bytes, over the %d budget", stage, added, DefaultIncludeBudget)
		}
	}
}
