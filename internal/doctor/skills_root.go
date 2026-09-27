package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// checkSkillsRoot reports whether THIS binary, run from startDir's repository,
// can locate every stage's SKILL.md through skillrender.DefaultRoots — the
// same roots `nightgauge run` and the scheduler render from (#2220). It is
// offline and costs one stat per candidate. A miss is a warning naming the
// roots searched and the two remedies, because a run fails in seconds on it.
func checkSkillsRoot(startDir string) (CheckItem, string) {
	return checkSkillsRootIn(skillrender.DefaultRoots(repoRootOf(startDir)))
}

func checkSkillsRootIn(roots []string) (CheckItem, string) {
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
		return CheckItem{OK: true, Detail: fmt.Sprintf("all %d stage skills located (resolved from %s; searched %s)",
			len(stages), strings.Join(used, ", "), searched)}, ""
	}
	msg := fmt.Sprintf("skills: this binary cannot locate SKILL.md for %s (searched %s). "+
		"A run needing these stages fails before dispatch. Fix: install the bundle layout "+
		"<prefix>/bin/nightgauge beside <prefix>/skills/, or set %s to a skills tree",
		strings.Join(missing, ", "), searched, skillrender.SkillsRootEnv)
	return CheckItem{OK: false, Error: msg}, msg
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
