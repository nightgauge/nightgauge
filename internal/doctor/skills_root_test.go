package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

func writeSkills(t *testing.T, root string, stages ...string) {
	t.Helper()
	for _, stage := range stages {
		dir := filepath.Join(root, skillrender.StageSkillDirs[stage])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# s\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckSkillsRootPassesWhenEveryStageResolves(t *testing.T) {
	root := t.TempDir()
	var all []string
	for stage := range skillrender.StageSkillDirs {
		all = append(all, stage)
	}
	writeSkills(t, root, all...)

	item, warning := checkSkillsRootIn([]string{filepath.Join(t.TempDir(), "skills"), root})
	if !item.OK || warning != "" {
		t.Fatalf("item = %+v, warning = %q; want OK", item, warning)
	}
	if !strings.Contains(item.Detail, root) {
		t.Errorf("detail %q does not report the resolving root %q", item.Detail, root)
	}
}

// TestCheckSkillsRootWarnsWithRootsAndRemedy is #2220's doctor arm: a binary
// that cannot find issue-pickup is reported with every root searched and both
// fixes, instead of surfacing as a 2-second validation_error mid-run.
func TestCheckSkillsRootWarnsWithRootsAndRemedy(t *testing.T) {
	a := filepath.Join(t.TempDir(), "skills")
	b := t.TempDir()
	writeSkills(t, b, "feature-dev")

	item, warning := checkSkillsRootIn([]string{a, b})
	if item.OK || warning == "" {
		t.Fatalf("item = %+v; want a warning", item)
	}
	for _, want := range []string{"issue-pickup", a, b, "<prefix>/skills/", skillrender.SkillsRootEnv} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q lacks %q", warning, want)
		}
	}
	if strings.Contains(warning, "feature-dev,") || strings.HasPrefix(warning, "feature-dev") {
		t.Errorf("warning lists feature-dev, which resolved: %q", warning)
	}
}

func TestCheckSkillsRootReadsTheEnvOverride(t *testing.T) {
	override := t.TempDir()
	var all []string
	for stage := range skillrender.StageSkillDirs {
		all = append(all, stage)
	}
	writeSkills(t, override, all...)
	t.Setenv(skillrender.SkillsRootEnv, override)

	item, _ := checkSkillsRoot(t.TempDir())
	if !item.OK {
		t.Fatalf("with %s set: %+v", skillrender.SkillsRootEnv, item)
	}
}

func TestRepoRootOfWalksToGit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repoRootOf(sub); got != root {
		t.Errorf("repoRootOf = %q, want %q", got, root)
	}
}
