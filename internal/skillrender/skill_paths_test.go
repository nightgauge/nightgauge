package skillrender

import (
	"path/filepath"
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
