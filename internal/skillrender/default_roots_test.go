package skillrender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeBundle builds the extension's packaging layout — <bundle>/dist/bin/nightgauge
// beside <bundle>/dist/skills/ — and returns the executable path.
func fakeBundle(t *testing.T, withSkills bool) string {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "dist", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "nightgauge")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withSkills {
		if err := os.MkdirAll(filepath.Join(root, "dist", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

// TestDefaultRootsListsTheBundleTree is the regression for #874.
//
// The Go-direct path (`queue run`, the autonomous scheduler) had exactly one
// root, `{workspaceRoot}/skills`, which exists in this repository and nowhere
// else. Every stage in every user's repo failed to render its SKILL.md while
// the file sat in the bundle one directory above the running binary.
func TestDefaultRootsListsTheBundleTree(t *testing.T) {
	exe := fakeBundle(t, true)
	bundleSkills := filepath.Join(filepath.Dir(filepath.Dir(exe)), "skills")

	roots := defaultRoots("/repo", exe, "")

	if len(roots) != 2 {
		t.Fatalf("roots = %v, want the bundle tree and the workspace tree", roots)
	}
	// EvalSymlinks: macOS /var/folders temp dirs resolve through /private.
	wantBundle, err := filepath.EvalSymlinks(bundleSkills)
	if err != nil {
		wantBundle = bundleSkills
	}
	if roots[0] != wantBundle {
		t.Errorf("roots[0] = %q, want the bundle tree %q", roots[0], wantBundle)
	}
	if roots[1] != filepath.Join("/repo", "skills") {
		t.Errorf("roots[1] = %q, want the workspace tree last", roots[1])
	}
}

// TestDefaultRootsOmitsAMissingBundleTree answers DefaultRoots's own objection:
// a root that cannot match must never be returned, so a plain `go build` binary
// with no bundle beside it keeps exactly one root.
func TestDefaultRootsOmitsAMissingBundleTree(t *testing.T) {
	exe := fakeBundle(t, false)

	roots := defaultRoots("/repo", exe, "")

	if len(roots) != 1 {
		t.Fatalf("roots = %v, want only the workspace tree when no bundle exists", roots)
	}
}

// TestDefaultRootsSurvivesAnUnknownExecutable pins the degenerate arm:
// os.Executable can fail, and a render must still search the workspace.
func TestDefaultRootsSurvivesAnUnknownExecutable(t *testing.T) {
	roots := defaultRoots("/repo", "", "")

	if len(roots) != 1 || roots[0] != filepath.Join("/repo", "skills") {
		t.Fatalf("roots = %v, want just the workspace tree", roots)
	}
}

// rootOrderVector is one case of skill-root-order.json, which the extension's
// host facts reach through `nightgauge skill roots`. Want lists the tokens
// override, bundle and workspace.
type rootOrderVector struct {
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Override    bool     `json:"override"`
	Bundle      bool     `json:"bundle"`
	NoWorkspace bool     `json:"no_workspace"`
	Want        []string `json:"want"`
}

const rootOrderVectorFile = "skill-root-order.json"

func rootOrderVectors(t *testing.T) []rootOrderVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", rootOrderVectorFile))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []rootOrderVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse %s: %v", rootOrderVectorFile, err)
	}
	return vectors
}

// TestResolveRootsMatchesTheVectors pins #2718's order for every host: override,
// bundle, workspace, with the last two swapped for a development host.
func TestResolveRootsMatchesTheVectors(t *testing.T) {
	const override, bundle = "/operator/skills", "/ext/dist/skills"
	tokens := map[string]string{
		"override":  override,
		"bundle":    bundle,
		"workspace": filepath.Join("/repo", "skills"),
	}
	modes := map[string]int{}
	for _, v := range rootOrderVectors(t) {
		modes[v.Mode]++
		t.Run(v.Name, func(t *testing.T) {
			in := RootInputs{Workspace: "/repo", WorkspaceFirst: v.Mode == "development"}
			if v.NoWorkspace {
				in.Workspace = ""
			}
			if v.Override {
				in.Override = override
			}
			if v.Bundle {
				in.Bundle = bundle
			}
			want := make([]string, 0, len(v.Want))
			for _, token := range v.Want {
				want = append(want, tokens[token])
			}
			if got := ResolveRoots(in); !slices.Equal(got, want) {
				t.Errorf("roots = %v, want %v", got, want)
			}
		})
	}
	for _, mode := range []string{"production", "development"} {
		if modes[mode] == 0 {
			t.Errorf("%s has no %s vectors", rootOrderVectorFile, mode)
		}
	}
}

// TestSkillRunnerLeavesRootOrderToTheBinary is what stops the second resolver
// coming back. #874 passed Go's root tests throughout its life because the two
// hosts resolved independently, so the extension may gather facts but not order
// them, and may not read the override the binary already reads.
func TestSkillRunnerLeavesRootOrderToTheBinary(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..", "..", "packages", "nightgauge", "src", "utils", "skillRunner.ts"))
	if err != nil {
		t.Fatalf("read skillRunner.ts: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "env."+SkillsRootEnv) {
		t.Errorf("skillRunner.ts reads %s; the binary owns the override", SkillsRootEnv)
	}
	_, rest, found := strings.Cut(src, "export function resolveSkillRoots(")
	if !found {
		t.Fatal("skillRunner.ts no longer defines resolveSkillRoots")
	}
	body, _, found := strings.Cut(rest, "\n}\n")
	if !found {
		t.Fatal("cannot find the end of resolveSkillRoots")
	}
	if strings.Contains(body, "roots.push") {
		t.Error("resolveSkillRoots orders roots itself; ResolveRoots owns the order")
	}
	if !strings.Contains(body, `"roots"`) {
		t.Error("resolveSkillRoots does not ask `nightgauge skill roots`")
	}
}

// TestLocateFallsBackToTheWorkspace: the order changed in #2718, the root list
// did not. A skill the bundle does not ship still resolves from the workspace,
// and one both hold resolves from the bundle.
func TestLocateFallsBackToTheWorkspace(t *testing.T) {
	exe := fakeBundle(t, true)
	bundle := filepath.Join(filepath.Dir(filepath.Dir(exe)), "skills")
	workspace := t.TempDir()
	write := func(root, stage string) string {
		dir := filepath.Join(root, StageSkillDirs[stage])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(p, []byte("# skill\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	onlyWorkspace := write(filepath.Join(workspace, "skills"), "issue-pickup")
	write(filepath.Join(workspace, "skills"), "feature-dev")
	bundled := write(bundle, "feature-dev")
	roots := defaultRoots(workspace, exe, "")

	got, err := Locate("issue-pickup", roots)
	if err != nil {
		t.Fatalf("Locate a workspace-only skill: %v", err)
	}
	if got != onlyWorkspace {
		t.Errorf("workspace-only skill = %q, want %q", got, onlyWorkspace)
	}
	got, err = Locate("feature-dev", roots)
	if err != nil {
		t.Fatalf("Locate a bundled skill: %v", err)
	}
	// EvalSymlinks: DefaultRoots resolves the bundle, and macOS temp dirs are links.
	want, err := filepath.EvalSymlinks(bundled)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("skill in both trees = %q, want the bundle's %q", got, want)
	}
}

// TestDefaultRootsKeepsAMissingOverride: an explicit override is listed even
// when it does not exist, so a typo shows in the not-found error.
func TestDefaultRootsKeepsAMissingOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	roots := defaultRoots("/repo", "", "  "+missing+" ")
	if len(roots) != 2 || roots[0] != missing {
		t.Fatalf("roots = %v, want %q and the workspace tree", roots, missing)
	}
	if got := defaultRoots("/repo", "", "   "); len(got) != 1 {
		t.Fatalf("blank override: roots = %v, want only the workspace tree", got)
	}
}

// TestDefaultRootsReadsTheEnvOverride proves the exported entry point reads
// NIGHTGAUGE_SKILLS_ROOT and that Locate resolves a stage through it — the
// exact failure #2220 reported from a repository without skills/.
func TestDefaultRootsReadsTheEnvOverride(t *testing.T) {
	override := t.TempDir()
	skillDir := filepath.Join(override, StageSkillDirs["issue-pickup"])
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# pickup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SkillsRootEnv, override)

	workspace := t.TempDir() // a repository with no skills/ tree
	roots := DefaultRoots(workspace)
	if len(roots) < 2 || roots[0] != override {
		t.Fatalf("DefaultRoots = %v, want %q first", roots, override)
	}
	path, err := Locate("issue-pickup", roots)
	if err != nil {
		t.Fatalf("Locate through %s: %v", SkillsRootEnv, err)
	}
	if !strings.HasPrefix(path, override) && !strings.Contains(path, filepath.Base(override)) {
		t.Errorf("path = %q, want it under the override %q", path, override)
	}
}

// TestLocateErrorNamesRootsAndFixes pins the not-found text: every searched
// root, and both remedies (bundle layout, NIGHTGAUGE_SKILLS_ROOT).
func TestLocateErrorNamesRootsAndFixes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	_, err := Locate("issue-pickup", []string{a, b})
	if err == nil {
		t.Fatal("Locate succeeded over empty roots")
	}
	msg := err.Error()
	for _, want := range []string{
		`SKILL.md not found for stage "issue-pickup"`,
		"searched roots " + a + ", " + b,
		"<prefix>/bin/nightgauge beside <prefix>/skills/",
		SkillsRootEnv,
		"nightgauge-issue-pickup/SKILL.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}
