package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bundleScript is the marketplace bundler the VSIX is built from. Reading a
// sibling package's shell script from Go is the same cross-file idiom
// TestGoAndTypeScriptAgreeOnSkillRootOrder already uses to pin the root order
// against skillRunner.ts — the two halves of a contract live in two languages,
// so the guard has to reach across.
const bundleScript = "../../packages/nightgauge-vscode/scripts/bundle-marketplace.sh"

// TestBundleShipsEverySkillTheGoDirectPathRenders is the guard #1029 needed and
// did not have.
//
// StageSkillDirs is what lets the binary NAME a skill; the bundle list is what
// puts the file on disk next to the binary. When they disagree, the skill
// resolves in the nightgauge source tree — where <workspaceRoot>/skills exists
// — and nowhere else. That is invisible in this repository and total in every
// other one, which is exactly why it survived #874 and shipped.
func TestBundleShipsEverySkillTheGoDirectPathRenders(t *testing.T) {
	raw, err := os.ReadFile(bundleScript)
	if err != nil {
		t.Fatalf("read %s: %v", bundleScript, err)
	}
	bundled := parseBundledSkills(t, string(raw))
	if len(bundled) == 0 {
		t.Fatalf("parsed no skills out of %s — the `for skill in ...` loop moved; fix this parser", bundleScript)
	}

	for stage, dir := range StageSkillDirs {
		if !bundled[dir] {
			t.Errorf("StageSkillDirs[%q] = %q, but the marketplace bundle does not ship it.\n"+
				"The binary can name this skill and the VSIX cannot supply it, so it resolves\n"+
				"only in a workspace that happens to hold a skills/ tree.\n"+
				"Add %s to the `for skill in ...` list in %s.",
				stage, dir, dir, bundleScript)
		}
	}
}

// TestBundledSkillsExistOnDisk keeps the list honest in the other direction: a
// typo'd or renamed directory is silently skipped by the bundler's `if [ -d ]`
// guard, producing a VSIX that is quietly missing a skill.
func TestBundledSkillsExistOnDisk(t *testing.T) {
	raw, err := os.ReadFile(bundleScript)
	if err != nil {
		t.Fatalf("read %s: %v", bundleScript, err)
	}
	for dir := range parseBundledSkills(t, string(raw)) {
		p := filepath.Join("../../skills", dir, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("bundle list names %q but %s does not exist: the bundler's `if [ -d ]` guard skips it silently", dir, p)
		}
	}
}

var bundleLoopRE = regexp.MustCompile(`(?s)for skill in\s+(.*?);\s*do`)

func parseBundledSkills(t *testing.T, script string) map[string]bool {
	t.Helper()
	m := bundleLoopRE.FindStringSubmatch(script)
	if m == nil {
		return nil
	}
	out := map[string]bool{}
	for _, f := range strings.Fields(strings.ReplaceAll(m[1], "\\", " ")) {
		if f != "" {
			out[f] = true
		}
	}
	return out
}
