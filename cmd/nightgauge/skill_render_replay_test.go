package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillRenderReplayFlags pins the two flags a stage replay uses to print
// the exact stage prompt: --supply-includes supplies a compact render's
// phase includes (the scheduler's local-endpoint render), and --issue wraps
// the render in execution.BuildPrompt's invocation context.
func TestSkillRenderReplayFlags(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	base := []string{"--stage", "feature-validate", "--skills-root", root, "--profile", "compact"}

	plain, err := runRender(t, base...)
	if err != nil {
		t.Fatalf("plain compact render: %v", err)
	}
	supplied, err := runRender(t, append(base, "--supply-includes")...)
	if err != nil {
		t.Fatalf("--supply-includes: %v", err)
	}
	if len(supplied) <= len(plain) {
		t.Errorf("--supply-includes should add the phase includes: %d bytes vs %d without", len(supplied), len(plain))
	}
	if strings.Contains(plain, "## Invocation Context") {
		t.Error("without --issue the render must stay the bare skill")
	}

	wrapped, err := runRender(t, append(base, "--issue", "4", "--context-type", "dev", "--context-file", "/wt/.nightgauge/pipeline/dev-4.json")...)
	if err != nil {
		t.Fatalf("--issue: %v", err)
	}
	for _, want := range []string{"## Invocation Context", "- **Issue**: #4", "- **Stage**: feature-validate",
		"- **Input context type**: dev", "- **Input context file**: /wt/.nightgauge/pipeline/dev-4.json"} {
		if !strings.Contains(wrapped, want) {
			t.Errorf("--issue render is missing %q", want)
		}
	}
	if !strings.HasPrefix(wrapped, plain) {
		t.Error("the invocation context must come after the unchanged skill body")
	}
}
