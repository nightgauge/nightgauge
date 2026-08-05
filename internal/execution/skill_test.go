package execution

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// BuildPrompt annotates the skill directory but must NOT rewrite paths: the
// content it receives has already been rewritten by skillrender.Render (ADR
// 016 §4). A second rewrite is not idempotent — see the comment in skill.go.
func TestBuildPrompt_AnnotatesSkillDirWithoutRewriting(t *testing.T) {
	alreadyRewritten := "Read `/abs/skills/nightgauge-feature-dev/_includes/plan.md` now."
	prompt := BuildPrompt(state.StageFeatureDev, alreadyRewritten,
		7, "/abs/skills/nightgauge-feature-dev", "", "")

	if !strings.Contains(prompt, "/abs/skills/nightgauge-feature-dev/_includes/plan.md") {
		t.Errorf("composed path did not survive: %s", prompt)
	}
	if strings.Contains(prompt, "/abs//abs/") {
		t.Errorf("path was rewritten a second time and is now corrupt: %s", prompt)
	}
	if !strings.Contains(prompt, "**Skill directory**: /abs/skills/nightgauge-feature-dev") {
		t.Errorf("invocation context missing skill directory: %s", prompt)
	}
}
func TestBuildPrompt(t *testing.T) {
	prompt := BuildPrompt(state.StageFeatureDev, "# Do the feature dev", 1234, "",
		"planning", ".nightgauge/pipeline/planning-1234.json")

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
	if !strings.Contains(prompt, "Input context type**: planning") {
		t.Error("prompt should surface the resolved input context type")
	}
	if !strings.Contains(prompt, ".nightgauge/pipeline/planning-1234.json") {
		t.Error("prompt should surface the resolved input context file")
	}

	// Stable-prefix-first ordering (#3805): the skill body must precede the
	// variable invocation context block so it forms the cacheable prefix.
	skillIdx := strings.Index(prompt, "# Do the feature dev")
	ctxIdx := strings.Index(prompt, "## Invocation Context")
	if skillIdx < 0 || ctxIdx < 0 || skillIdx > ctxIdx {
		t.Error("skill content must precede invocation context (stable-prefix-first, #3805)")
	}
}
func TestBuildPrompt_FastTracked(t *testing.T) {
	prompt := BuildPrompt(state.StageFeatureDev, "# Do the feature dev", 48, "",
		"issue", ".nightgauge/pipeline/issue-48.json")

	if !strings.Contains(prompt, "Input context type**: issue") {
		t.Error("prompt should surface fast-tracked input context type")
	}
	if !strings.Contains(prompt, ".nightgauge/pipeline/issue-48.json") {
		t.Error("prompt should surface the issue context file, not a planning path")
	}
	if strings.Contains(prompt, "planning-48.json") {
		t.Error("prompt should not reference a planning context file when fast-tracked")
	}
}
