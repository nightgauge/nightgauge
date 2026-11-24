package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// feature-dev reads only this run's handoffs: its skill never names the
// validate handoff, which a card retry archives (#3565), and its declared
// inputs stay the planning context and the plan.
func TestFeatureDevSkill_ReadsOnlyCurrentRunHandoffs(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "skills", "nightgauge-feature-dev")
	files, err := filepath.Glob(filepath.Join(dir, "_includes", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join(dir, "SKILL.md"))
	ref := regexp.MustCompile(`validate-(\{N\}|\$\{ISSUE_NUMBER\}|\$ISSUE_NUMBER|%d)`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := ref.FindString(string(data)); m != "" {
			t.Errorf("%s references %q; feature-dev must not read the validate handoff", f, m)
		}
	}

	skill, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^inputs:\n((?:  - .*\n)+)`).FindStringSubmatch(string(skill))
	if m == nil {
		t.Fatal("SKILL.md frontmatter has no inputs: list")
	}
	got := strings.TrimSpace(m[1])
	want := "- pipeline/planning-{N}.json\n  - plans/{N}-*.md"
	if got != want {
		t.Errorf("feature-dev inputs = %q, want %q", got, want)
	}
}
