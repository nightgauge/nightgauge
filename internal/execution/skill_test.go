package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

func TestStageSkillDirs(t *testing.T) {
	// All 6 pipeline stages should have skill directories
	stages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	for _, stage := range stages {
		dir, ok := StageSkillDirs[stage]
		if !ok || dir == "" {
			t.Errorf("missing skill dir for stage %q", stage)
		}
		if !strings.HasPrefix(dir, "nightgauge-") {
			t.Errorf("skill dir %q should start with 'nightgauge-'", dir)
		}
	}
}

func TestReadSkillFile(t *testing.T) {
	// Create a temp SKILL.md with frontmatter
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	content := `---
name: test-skill
allowed-tools: Read Edit Bash AskUserQuestion
programmatic-tools: TodoWrite
---

# Test Skill

Do the thing.
`
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := ReadSkillFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}

	if data.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", data.Name, "test-skill")
	}

	// AskUserQuestion should be filtered out
	if len(data.AllowedTools) != 3 {
		t.Errorf("AllowedTools = %v, want [Read Edit Bash]", data.AllowedTools)
	}
	for _, tool := range data.AllowedTools {
		if tool == "AskUserQuestion" {
			t.Error("AskUserQuestion should be filtered out")
		}
	}

	if len(data.ProgrammaticTools) != 1 || data.ProgrammaticTools[0] != "TodoWrite" {
		t.Errorf("ProgrammaticTools = %v", data.ProgrammaticTools)
	}

	if !strings.Contains(data.Content, "# Test Skill") {
		t.Error("content should contain skill body")
	}
}

func TestReadSkillFileWithIncludes(t *testing.T) {
	dir := t.TempDir()

	// Create shared include file
	sharedDir := filepath.Join(dir, "_shared")
	os.MkdirAll(sharedDir, 0755)
	os.WriteFile(filepath.Join(sharedDir, "CONTEXT.md"), []byte("## Shared Context\nThis is shared."), 0644)

	// Create SKILL.md that includes it
	skillPath := filepath.Join(dir, "SKILL.md")
	content := `---
name: test-include
allowed-tools: Read
---

<!-- include: _shared/CONTEXT.md -->

# Main Content
`
	os.WriteFile(skillPath, []byte(content), 0644)

	data, err := ReadSkillFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(data.Content, "## Shared Context") {
		t.Error("include should be expanded")
	}
	if !strings.Contains(data.Content, "This is shared.") {
		t.Error("include content missing")
	}
	if !strings.Contains(data.Content, "# Main Content") {
		t.Error("main content missing")
	}
}

func TestRewriteSkillRelativePaths(t *testing.T) {
	content := "Read `skills/nightgauge-feature-dev/_includes/plan.md` now.\n" +
		"Also see skills/_shared/GOTCHAS.md and skills/feature-dev/_includes/x.md.\n" +
		"Cross-skill ref: skills/nightgauge-pipeline-audit/SKILL.md stays put.\n"
	got := RewriteSkillRelativePaths(content, state.StageFeatureDev, "/bundle/dist/skills/nightgauge-feature-dev")

	for _, want := range []string{
		"/bundle/dist/skills/nightgauge-feature-dev/_includes/plan.md",
		"/bundle/dist/skills/_shared/GOTCHAS.md",
		"/bundle/dist/skills/nightgauge-feature-dev/_includes/x.md", // prefix-stripped variant
		"skills/nightgauge-pipeline-audit/SKILL.md",                 // cross-skill ref untouched
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`skills/nightgauge-feature-dev/") {
		t.Errorf("own-skill relative path survived the rewrite:\n%s", got)
	}
}

func TestBuildPrompt_SkillDirRewritesAndAnnotates(t *testing.T) {
	prompt := BuildPrompt(state.StageFeatureDev,
		"Read `skills/nightgauge-feature-dev/_includes/plan.md` now.",
		7, "/abs/skills/nightgauge-feature-dev")
	if !strings.Contains(prompt, "/abs/skills/nightgauge-feature-dev/_includes/plan.md") {
		t.Errorf("read directive not rewritten: %s", prompt)
	}
	if !strings.Contains(prompt, "**Skill directory**: /abs/skills/nightgauge-feature-dev") {
		t.Errorf("invocation context missing skill directory: %s", prompt)
	}
}

func TestBuildPrompt(t *testing.T) {
	prompt := BuildPrompt(state.StageFeatureDev, "# Do the feature dev", 1234, "")

	if !strings.Contains(prompt, "#1234") {
		t.Error("prompt should contain issue number")
	}
	if !strings.Contains(prompt, "feature-dev") {
		t.Error("prompt should contain stage name")
	}
	if !strings.Contains(prompt, "headless") {
		t.Error("prompt should indicate headless mode")
	}
	if !strings.Contains(prompt, "# Do the feature dev") {
		t.Error("prompt should contain skill content")
	}

	// Stable-prefix-first ordering (#3805): the skill body must precede the
	// variable invocation context block so it forms the cacheable prefix.
	skillIdx := strings.Index(prompt, "# Do the feature dev")
	ctxIdx := strings.Index(prompt, "## Invocation Context")
	if skillIdx < 0 || ctxIdx < 0 || skillIdx > ctxIdx {
		t.Error("skill content must precede invocation context (stable-prefix-first, #3805)")
	}
}

func TestFindSkillFile(t *testing.T) {
	dir := t.TempDir()

	// Create skill directory structure
	skillDir := filepath.Join(dir, "skills", "nightgauge-issue-pickup")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Issue Pickup"), 0644)

	path, err := FindSkillFile(dir, state.StageIssuePickup)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "SKILL.md") {
		t.Errorf("path = %q", path)
	}

	// Missing skill should error
	_, err = FindSkillFile(dir, state.StageFeatureDev)
	if err == nil {
		t.Error("expected error for missing skill")
	}
}

func TestSplitTools(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"Read Edit Bash", 3},
		{"Read Edit Bash AskUserQuestion", 3}, // Filtered
		{"", 0},
		{"Read", 1},
	}
	for _, tt := range tests {
		got := splitTools(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitTools(%q) = %d tools, want %d", tt.input, len(got), tt.want)
		}
	}
}
