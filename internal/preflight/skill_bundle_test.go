package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

func writeBundleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// completeBundle writes a tree every rule passes on, in a directory named
// `skills` so a stray residual path behaves as it does in a real root.
func completeBundle(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills")
	writeBundleFile(t, filepath.Join(root, "_shared", "PREFLIGHT.md"), "preflight\n")
	writeBundleFile(t, filepath.Join(root, "_shared", "_overlays", "anthropic.md"), "shared overlay\n")
	for _, dir := range skillrender.StageSkillDirs {
		skill := filepath.Join(root, dir)
		writeBundleFile(t, filepath.Join(skill, "SKILL.md"),
			"---\nname: x\n---\n<!-- include: ../_shared/PREFLIGHT.md -->\n"+
				"> **Read `_includes/step.md` (same directory as this SKILL.md) now.**\n")
		writeBundleFile(t, filepath.Join(skill, "_includes", "step.md"), "step\n")
		writeBundleFile(t, filepath.Join(skill, "_overlays", "claude-fable-5-1.md"), "fable\n")
	}
	return root
}

func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "skills")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func runBundle(t *testing.T, bundle, source string) *SkillBundleResult {
	t.Helper()
	r, err := RunSkillBundleCheck(context.Background(), SkillBundleOptions{SkillsRoot: bundle, SourceRoot: source})
	if err != nil {
		t.Fatalf("RunSkillBundleCheck: %v", err)
	}
	return r
}

func hasFinding(r *SkillBundleResult, rule, text string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule && (strings.Contains(f.Path, text) || strings.Contains(f.Detail, text)) {
			return true
		}
	}
	return false
}

func TestSkillBundleCleanTree(t *testing.T) {
	root := completeBundle(t)
	r := runBundle(t, root, copyTree(t, root))
	if len(r.Findings) != 0 {
		t.Fatalf("findings on a complete tree: %+v", r.Findings)
	}
	if r.RendersChecked == 0 || r.FilesChecked == 0 {
		t.Fatalf("checked nothing: %+v", r)
	}
}

func TestSkillBundleFindsEachFault(t *testing.T) {
	feature := skillrender.StageSkillDirs["feature-dev"]
	for _, tt := range []struct {
		name  string
		fault func(t *testing.T, root string)
		rule  string
		text  string
	}{
		{
			name: "removed skill overlay",
			fault: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, feature, "_overlays")); err != nil {
					t.Fatal(err)
				}
			},
			rule: SkillBundleRuleFileSet, text: feature + "/_overlays/claude-fable-5-1.md",
		},
		{
			name: "removed _shared",
			fault: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, "_shared")); err != nil {
					t.Fatal(err)
				}
			},
			rule: SkillBundleRuleRenderFailed, text: "../_shared/PREFLIGHT.md",
		},
		{
			name: "removed _includes",
			fault: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, feature, "_includes")); err != nil {
					t.Fatal(err)
				}
			},
			rule: SkillBundleRuleRenderFailed, text: filepath.Join("_includes", "step.md"),
		},
		{
			name: "unresolved skill path",
			fault: func(t *testing.T, root string) {
				writeBundleFile(t, filepath.Join(root, feature, "SKILL.md"),
					"---\nname: x\n---\nfollow skills/nightgauge-absent/SKILL.md\n")
			},
			rule: SkillBundleRuleUnresolvedPath, text: "skills/nightgauge-absent/SKILL.md",
		},
		{
			name: "dead include in an unrendered profile file",
			fault: func(t *testing.T, root string) {
				writeBundleFile(t, filepath.Join(root, feature, "_overlays", "not-a-model.md"),
					"<!-- include: ../_shared/ABSENT.md -->\n")
			},
			rule: SkillBundleRuleDeadInclude, text: "../_shared/ABSENT.md",
		},
		{
			name: "extra file",
			fault: func(t *testing.T, root string) {
				writeBundleFile(t, filepath.Join(root, feature, "_includes", "stale.md"), "old\n")
			},
			rule: SkillBundleRuleFileSet, text: feature + "/_includes/stale.md",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := completeBundle(t)
			bundle := copyTree(t, source)
			tt.fault(t, bundle)
			r := runBundle(t, bundle, source)
			if !hasFinding(r, tt.rule, tt.text) {
				t.Fatalf("no %s finding naming %q: %+v", tt.rule, tt.text, r.Findings)
			}
		})
	}
}

func TestSkillBundleEmptyTreeFailsEveryStage(t *testing.T) {
	r := runBundle(t, t.TempDir(), "")
	for stage := range skillrender.StageSkillDirs {
		if !hasFinding(r, SkillBundleRuleRenderFailed, stage) {
			t.Errorf("stage %s not reported on an empty tree", stage)
		}
	}
}

func TestSkillBundleAbsentTreeCouldNotRun(t *testing.T) {
	_, err := RunSkillBundleCheck(context.Background(), SkillBundleOptions{SkillsRoot: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("an absent tree must not pass")
	}
}

func TestSkillBundleRendersEveryOverlayKind(t *testing.T) {
	root := completeBundle(t)
	feature := filepath.Join(root, skillrender.StageSkillDirs["feature-dev"], "_overlays")
	writeBundleFile(t, filepath.Join(feature, "claude-opus-5-5.SKILL.md"), "---\nname: y\n---\nbody\n")
	writeBundleFile(t, filepath.Join(root, "_shared", "_overlays", "openai.md"), "o\n")
	writeBundleFile(t, filepath.Join(root, "_shared", "_overlays", "hosts", "opencode.md"), "h\n")

	seen := map[string]bool{}
	for _, r := range bundleRenders(root) {
		if r.stage == "feature-dev" {
			seen[r.String()] = true
		}
	}
	for _, want := range []string{
		"feature-dev",
		"feature-dev profile=compact",
		"feature-dev model=claude-fable-5-1",
		"feature-dev model=claude-opus-5-5", // .SKILL.md stripped
		"feature-dev adapter=opencode",
	} {
		if !seen[want] {
			t.Errorf("no render %q in %v", want, seen)
		}
	}
	// A provider key must select a real model of that provider, not base-only.
	if got := modelForOverlayKey("openai"); got == "openai" {
		t.Errorf("modelForOverlayKey(openai) = %q, want a model id", got)
	}
}
