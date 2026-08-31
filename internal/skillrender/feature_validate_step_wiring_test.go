package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A step that exists in an include but is not named by the SKILL.md directive
// that invokes the include is DEAD. The model follows the directive as written.
//
// This is not hypothetical. #1233 added Step 0.6.2b — the step that makes the AC
// completion gate satisfiable at all — to
// `_includes/context-load.md`, and left the Phase 0.6 directive saying
// "Steps 0.6.2 (run ac-check) and 0.6.3 (gate on result) live in the include".
// Every unit test passed, the rendered-skill gate harness passed, the full CI
// gate passed, and the step never ran once in production: the live stage went
// ac-check → gate, exactly as instructed, and the deadlock the issue existed to
// fix was still there.
//
// The gate harness could not catch it by construction — it extracts fenced bash
// from the include and runs it directly, so it proves the step WORKS while
// saying nothing about whether anything INVOKES it. That is the
// `unpinned-wiring` defect class in docs/FAILURE_TAXONOMY.md: correct code,
// never reached.
func TestIncludeStepsAreNamedByTheirSkillDirective(t *testing.T) {
	root := filepath.Join("..", "..")
	skillPath := filepath.Join(root, "skills", "nightgauge-feature-validate", "SKILL.md")
	includePath := filepath.Join(root, filepath.FromSlash(acContextLoadRel))

	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	include, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatalf("read %s: %v", acContextLoadRel, err)
	}

	// Every "### Step N.N[suffix]:" heading the include defines.
	// Step numbers are multi-part with an optional letter suffix: 0.6.2, 0.6.2b.
	headingRe := regexp.MustCompile(`(?m)^### Step ([0-9]+(?:\.[0-9]+)+[a-z]?):`)
	matches := headingRe.FindAllStringSubmatch(string(include), -1)
	if len(matches) == 0 {
		t.Fatalf("no '### Step N.N:' headings found in %s — has the include been restructured?", acContextLoadRel)
	}

	skillText := string(skill)
	for _, m := range matches {
		step := m[1] // e.g. "0.6.2b"
		if !strings.Contains(skillText, step) {
			t.Errorf(
				"Step %s is defined in %s but never named in SKILL.md.\n"+
					"The model follows the directive as written, so this step will not run — "+
					"it is dead however well it is tested in isolation (#1233).",
				step, acContextLoadRel)
		}
	}
}

// The directive for a step must also survive the step being renamed: if the
// include's Phase 0.6 grows or loses a step, the count named in SKILL.md has to
// move with it. Pinning the specific step #1233 added, because it is the one
// whose absence is silent — the gate still "works", it just can never pass.
func TestACSubstantiationStepIsWired(t *testing.T) {
	root := filepath.Join("..", "..")
	skill, err := os.ReadFile(filepath.Join(root, "skills", "nightgauge-feature-validate", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(skill), "0.6.2b") {
		t.Fatal("Phase 0.6's directive does not name Step 0.6.2b — the AC gate reverts to " +
			"having no writer, and every type:docs issue with unticked criteria deadlocks again (#1233)")
	}
}
