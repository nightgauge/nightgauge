package skillrender

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestFeatureValidatePipelinePathUnchanged2851: removing the test-runner fork
// from feature-validate left the pipeline path alone. The pipeline render still
// grants the tools the stage needs to write its context file, the prompt still
// writes validate-{N}.json through the binary, and the stage's exit and shape
// gates are still in front of the agent.
func TestFeatureValidatePipelinePathUnchanged2851(t *testing.T) {
	root := realSkillsRoot(t)
	res := mustRender(t, Options{Stage: "feature-validate", SkillsRoots: []string{root}, Headless: true})

	for _, tool := range []string{"Bash", "Write", "Edit"} {
		if !slices.Contains(res.AllowedTools, tool) {
			t.Errorf("pipeline render lost %s; allowed tools = %v", tool, res.AllowedTools)
		}
	}
	for _, want := range []string{
		"nightgauge layout write pipeline validate-{N}.json",
		`layout path pipeline validate-${ISSUE_NUMBER}.json)" || \`,
		"Phase 6 was skipped",
		`name="write-validate-context"`,
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("rendered feature-validate prompt lacks %q", want)
		}
	}

	include, err := os.ReadFile(filepath.Join(root, "nightgauge-feature-validate", "_includes", "context-and-board.md"))
	if err != nil {
		t.Fatalf("read context include: %v", err)
	}
	for _, want := range []string{
		`layout write pipeline "validate-${ISSUE_NUMBER}.json"`,
		"gate check-deliverable --stage validate",
	} {
		if !strings.Contains(string(include), want) {
			t.Errorf("context-and-board.md lacks %q", want)
		}
	}
}
