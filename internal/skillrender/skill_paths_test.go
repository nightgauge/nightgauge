package skillrender

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillPaths(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "nightgauge-feature-dev")
	write(t, filepath.Join(skillDir, "_includes", "here.md"), "x\n")
	write(t, filepath.Join(root, "_shared", "GOTCHAS.md"), "x\n")
	here := filepath.Join(skillDir, "_includes", "here.md")
	gone := filepath.Join(skillDir, "_includes", "gone.md")

	for _, tt := range []struct {
		name    string
		content string
		want    []SkillPath
	}{
		{"absolute present", "Read `" + here + "` now.", []SkillPath{{here, here, true}}},
		{"absolute missing", "Read " + gone + ".", []SkillPath{{gone, gone, false}}},
		{"residual cross-skill", "see skills/nightgauge-retro/SKILL.md", []SkillPath{{"skills/nightgauge-retro/SKILL.md", "", false}}},
		{"residual shared", "`skills/_shared/GOTCHAS.md`", []SkillPath{{"skills/_shared/GOTCHAS.md", "", false}}},
		{"backticked include present", "`_includes/here.md`", []SkillPath{{"_includes/here.md", here, true}}},
		{"backticked include missing", "`_includes/gone.md`", []SkillPath{{"_includes/gone.md", gone, false}}},
		{"relative shared", "[x](../_shared/GOTCHAS.md)", []SkillPath{{"../_shared/GOTCHAS.md", filepath.Join(root, "_shared", "GOTCHAS.md"), true}}},
		{"relative cross-skill present", "[x](../nightgauge-feature-dev/_includes/here.md)", []SkillPath{{"../nightgauge-feature-dev/_includes/here.md", here, true}}},
		{"relative cross-skill outside bundle", "[x](../../nightgauge-verify-ui/SKILL.md)", []SkillPath{{"../../nightgauge-verify-ui/SKILL.md", filepath.Join(filepath.Dir(root), "nightgauge-verify-ui", "SKILL.md"), false}}},
		{"deep shared outside bundle", "[x](../../_shared/GOTCHAS.md)", []SkillPath{{"../../_shared/GOTCHAS.md", filepath.Join(filepath.Dir(root), "_shared", "GOTCHAS.md"), false}}},
		{"fragment stripped", "[x](../_shared/GOTCHAS.md#top)", []SkillPath{{"../_shared/GOTCHAS.md#top", filepath.Join(root, "_shared", "GOTCHAS.md#top"), true}}},
		{"repeat reported once", here + " and " + here, []SkillPath{{here, here, true}}},
		{"stage placeholder", "skills/nightgauge-{STAGE_NAME}/SKILL.md", nil},
		{"angle placeholder", filepath.Join(root, "<skill>", "x.md"), nil},
		{"glob", filepath.Join(skillDir, "_includes", "*.md"), nil},
		{"not a skill path", "../../docs/TESTING.md and docs/x.md", nil},
		{"longer word", "myskills/nightgauge-retro/SKILL.md", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := SkillPaths(tt.content, skillDir, root)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRewriteRelativeSkillRefs(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "nightgauge-feature-validate")
	write(t, filepath.Join(skillDir, "SKILL.md"), "x\n")
	write(t, filepath.Join(root, "nightgauge-verify-ui", "SKILL.md"), "x\n")
	write(t, filepath.Join(root, "_shared", "U.md"), "x\n")
	sep := string(filepath.Separator)
	ui := filepath.Join(root, "nightgauge-verify-ui") + sep
	shared := filepath.Join(root, "_shared") + sep
	for _, tt := range []struct{ name, in, want string }{
		{"depth 2 cross-skill", "[a](../../nightgauge-verify-ui/SKILL.md)", "[a](" + ui + "SKILL.md)"},
		{"depth 1 cross-skill", "[a](../nightgauge-verify-ui/SKILL.md)", "[a](" + ui + "SKILL.md)"},
		{"missing sibling kept", "[a](../../nightgauge-nope/SKILL.md)", "[a](../../nightgauge-nope/SKILL.md)"},
		{"deep shared", "[a](../../_shared/U.md)", "[a](" + shared + "U.md)"},
		{"single shared kept", "[a](../_shared/U.md)", "[a](../_shared/U.md)"},
		{"absolute kept", ui + "SKILL.md", ui + "SKILL.md"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := RewriteSkillRelativePaths(tt.in, "feature-validate", skillDir); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRealFeatureValidateVerifyUILinkResolves renders the shipped stage with
// every include supplied: the verify-ui link written relative to _includes/ must
// reach the real sibling once inlined (#2800).
func TestRealFeatureValidateVerifyUILinkResolves(t *testing.T) {
	root, err := filepath.Abs(realSkillsRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	res := mustRender(t, Options{Stage: "feature-validate", SkillsRoots: []string{root}, IncludeBudget: EveryPhaseInclude})
	want := filepath.Join(root, "nightgauge-verify-ui", "SKILL.md")
	if !strings.Contains(res.Content, "]("+want+")") {
		t.Errorf("verify-ui link target not rewritten to %s", want)
	}
	skillDir := filepath.Join(root, "nightgauge-feature-validate")
	for _, sp := range SkillPaths(res.Content, skillDir, root) {
		if !sp.Exists && strings.Contains(sp.Path, "nightgauge-verify-ui") {
			t.Errorf("dead verify-ui path in rendered prompt: %+v", sp)
		}
	}
}
