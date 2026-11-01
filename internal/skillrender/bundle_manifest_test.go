package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bundleScript is the marketplace bundler the VSIX is built from. Reading a
// sibling package's shell script from Go is the same cross-file idiom
// TestSkillRunnerLeavesRootOrderToTheBinary uses to keep the root order out of
// the extension — the two halves of a contract live in two languages, so the
// guard has to reach across.
const bundleScript = "../../packages/nightgauge/scripts/bundle-marketplace.sh"

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
// typo'd or renamed directory fails the bundler, so catch it here first.
func TestBundledSkillsExistOnDisk(t *testing.T) {
	raw, err := os.ReadFile(bundleScript)
	if err != nil {
		t.Fatalf("read %s: %v", bundleScript, err)
	}
	for dir := range parseBundledSkills(t, string(raw)) {
		p := filepath.Join("../../skills", dir, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("bundle list names %q but %s does not exist: the bundler exits 1 on it", dir, p)
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

// bundleSkills runs the real bundler and returns its dist/skills. A guard that
// only parsed the script could not see what it copies, which is how the overlays
// went missing (#2761). A missing rsync fails here: the script needs it, and a
// skipped guard is no guard.
func bundleSkills(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	cmd := exec.Command("bash", bundleScript)
	cmd.Env = append(os.Environ(), "NIGHTGAUGE_BUNDLE_DIST="+dist)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run %s: %v\n%s", bundleScript, err, out)
	}
	return filepath.Join(dist, "skills")
}

func TestBundleShipsEverySkillFile(t *testing.T) {
	bundle := bundleSkills(t)
	diffs, err := DiffBundleFiles("../../skills", bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		t.Errorf("%+v: bundle and source disagree on this file", d)
	}
	for _, name := range []string{
		"nightgauge-feature-dev/_overlays/claude-fable-5-1.md",
		"nightgauge-feature-validate/_overlays/claude-fable-5-1.md",
		"nightgauge-verify-ui/flows/dashboard-auth.md",
		"nightgauge-pattern-mining/SKILL.md",
		"_shared/_overlays/hosts/opencode.md",
	} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err != nil {
			t.Errorf("bundle lacks %s: %v", name, err)
		}
	}
	for _, name := range []string{"nightgauge-feature-dev/CHANGELOG.md", "nightgauge-pattern-mining/tests"} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err == nil {
			t.Errorf("bundle ships excluded %s", name)
		}
	}
}

// TestEverySkillPathInABundleRenderExists is the AC the source tree cannot
// answer: there, a file the bundle lacks still resolves.
func TestEverySkillPathInABundleRenderExists(t *testing.T) {
	root := bundleSkills(t)
	if len(StageSkillDirs) != 9 {
		t.Fatalf("StageSkillDirs has %d stages, want 9: extend this test", len(StageSkillDirs))
	}
	rendered, found := 0, 0
	for stage := range StageSkillDirs {
		for _, profile := range []string{"", ProfileCompact} {
			// Every include supplied: one over the default budget is still text
			// a stage is handed, and a verify-ui reference lives in one.
			res, err := Render(Options{
				Stage:          stage,
				Profile:        profile,
				Model:          "claude-fable-5-1",
				SkillsRoots:    []string{root},
				IncludeBudget:  EveryPhaseInclude,
				SupplyIncludes: true,
			})
			if err != nil {
				t.Errorf("%s profile=%q: %v", stage, profile, err)
				continue
			}
			rendered++
			paths := SkillPaths(res.Content, filepath.Dir(res.SkillPath), root)
			found += len(paths)
			for _, p := range paths {
				if !p.Exists {
					t.Errorf("%s profile=%q: prompt names %s, which does not exist under %s", stage, profile, p.Path, root)
				}
			}
		}
	}
	if rendered != 18 {
		t.Errorf("rendered %d, want 18", rendered)
	}
	if found == 0 {
		t.Error("no render named a skill path; the extractor stopped matching")
	}
	for stage, target := range map[string]string{
		"feature-planning": "nightgauge-pattern-mining",
		"feature-validate": "nightgauge-verify-ui",
	} {
		res, err := Render(Options{Stage: stage, SkillsRoots: []string{root}, IncludeBudget: EveryPhaseInclude})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Content, filepath.Join(root, target, "SKILL.md")) {
			t.Errorf("%s does not name %s under the bundle", stage, target)
		}
	}
}

func TestPackagingWorkflowsCheckTheExtractedBundle(t *testing.T) {
	for _, wf := range []string{"release.yml", "staging.yml"} {
		raw, err := os.ReadFile(filepath.Join("../../.github/workflows", wf))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, want := range []string{
			"unzip -q \"$VSIX\" 'extension/dist/skills/*'",
			"preflight skill-bundle",
			"--source-root \"$GITHUB_WORKSPACE/skills\"",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not contain %q", wf, want)
			}
		}
		build := strings.Index(text, "go build -o \"$RUNNER_TEMP/nightgauge-runner\"")
		verify := strings.Index(text, "- name: Verify VSIX contents")
		if build < 0 || build > verify {
			t.Errorf("%s must build the runner's binary before Verify VSIX contents", wf)
		}
	}
}
