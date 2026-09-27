package opencodeallow

import (
	"path/filepath"
	"testing"
)

func TestSkillsRoot(t *testing.T) {
	for _, tc := range []struct{ skill, want string }{
		{"/b/skills/nightgauge-feature-dev/SKILL.md", "/b/skills"},
		{"/b/skills/nightgauge-feature-dev.md", "/b/skills"},
		{"", ""},
	} {
		if got := SkillsRoot(Options{SkillPath: tc.skill}); got != tc.want {
			t.Errorf("SkillsRoot(%q) = %q, want %q", tc.skill, got, tc.want)
		}
	}
}

// The dispatch-time gate rebuilds the same roots from the child's env: the
// whole skills tree (#2191) and the knowledge base (#2194).
func TestRootsFromEnv_SkillsRootAndKnowledgeBase(t *testing.T) {
	t.Setenv("NIGHTGAUGE_SKILL_DIR", "/prefix/skills/nightgauge-feature-planning")
	t.Setenv("NIGHTGAUGE_CONTEXT_FILE", "")
	t.Setenv("NIGHTGAUGE_OUTPUT_FILE", "")
	t.Setenv(EnvKnowledgeDir, "/main/.nightgauge/knowledge")
	roots := RootsFromEnv("/main/.nightgauge/worktrees/x")
	has := func(r string) bool {
		for _, x := range roots {
			if x == r {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"/prefix/skills", "/main/.nightgauge/knowledge"} {
		if !has(want) {
			t.Errorf("roots %v missing %s", roots, want)
		}
	}

	t.Setenv(EnvKnowledgeDir, filepath.Join("relative", "kb"))
	for _, r := range RootsFromEnv("/w") {
		if r == filepath.Join("relative", "kb") {
			t.Errorf("a relative knowledge dir must not be allow-listed: %v", roots)
		}
	}
}
