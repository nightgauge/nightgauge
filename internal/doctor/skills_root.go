package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// skillsRootFindings reports whether THIS binary, run from startDir's repository,
// can locate every stage's SKILL.md through skillrender.DefaultRoots — the
// same roots `nightgauge run` and the scheduler render from (#2220). It is
// offline and costs one stat per candidate. A miss is a blocker (ADR-025 NGD002) naming the
// roots searched and the two remedies, because a run fails in seconds on it.
func skillsRootFindings(startDir string) ([]Finding, string) {
	return skillsRootFindingsIn(skillrender.DefaultRoots(repoRootOf(startDir)))
}

func skillsRootFindingsIn(roots []string) ([]Finding, string) {
	stages := make([]string, 0, len(skillrender.StageSkillDirs))
	for stage := range skillrender.StageSkillDirs {
		stages = append(stages, stage)
	}
	sort.Strings(stages)

	var missing []string
	found := map[string]bool{}
	for _, stage := range stages {
		path, err := skillrender.Locate(stage, roots)
		if err != nil {
			missing = append(missing, stage)
			continue
		}
		for _, root := range roots {
			if abs, err := filepath.Abs(root); err == nil && strings.HasPrefix(path, abs+string(filepath.Separator)) {
				found[abs] = true
				break
			}
		}
	}
	searched := strings.Join(roots, ", ")
	if len(missing) == 0 {
		var used []string
		for r := range found {
			used = append(used, r)
		}
		sort.Strings(used)
		return nil, fmt.Sprintf("all %d stage skills located (resolved from %s; searched %s)",
			len(stages), strings.Join(used, ", "), searched)
	}
	const check, code = "skills", "NGD002"
	return []Finding{newFinding(check, code, SeverityBlocker,
		"skills: this binary cannot locate SKILL.md for "+strings.Join(missing, ", "),
		fmt.Sprintf("searched %s. A run needing these stages fails before dispatch", searched),
		map[string]string{"missing": strings.Join(missing, ", "), "searched": searched},
		[]string{"skills-root"},
		manualRemedy("install", "Install the bundled skills tree or point "+skillrender.SkillsRootEnv+" at one", check,
			"Install the bundle layout <prefix>/bin/nightgauge beside <prefix>/skills/",
			"Or set "+skillrender.SkillsRootEnv+" to a skills tree"))}, "stage skills missing"
}

// repoRootOf walks up from dir to the nearest directory holding .git (a
// directory or a worktree's file); dir itself when there is none. Runs render
// against the repository root, so the doctor must too.
func repoRootOf(dir string) string {
	for d := dir; d != ""; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}
