package skillrender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// allowedToolsFixture is testdata/allowed_tools_expected.json, which the SDK's
// skillAllowedTools test reads too (#2358).
type allowedToolsFixture struct {
	Cases []struct {
		Name     string   `json:"name"`
		Skill    string   `json:"skill"`
		Declared []string `json:"declared"`
		Headless []string `json:"headless"`
		// MCP and Programmatic are checked only where a case has them.
		MCP          []string `json:"mcp"`
		Programmatic []string `json:"programmatic"`
	} `json:"cases"`
}

// TestAllowedToolsFixture pins the allowed-tools a SKILL.md declares and the
// ones a headless run is granted. The SDK's stage path reads every case of the
// same file through its own parser, so a grammar change made on one side only
// fails a test on the other.
func TestAllowedToolsFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "allowed_tools_expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture allowedToolsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("allowed_tools_expected.json: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("allowed_tools_expected.json has no cases")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, fm := splitFrontmatter(c.Skill)
			// nil and empty are the same answer here: the skill declares none.
			if !slices.Equal(fm.AllowedTools, c.Declared) {
				t.Errorf("declared = %q, want %q", fm.AllowedTools, c.Declared)
			}
			if got := FilterHeadlessTools(fm.AllowedTools); !slices.Equal(got, c.Headless) {
				t.Errorf("headless = %q, want %q", got, c.Headless)
			}
			if c.MCP != nil && !slices.Equal(fm.MCPTools, c.MCP) {
				t.Errorf("mcp-tools = %q, want %q", fm.MCPTools, c.MCP)
			}
			if c.Programmatic != nil && !slices.Equal(fm.ProgrammaticTools, c.Programmatic) {
				t.Errorf("programmatic-tools = %q, want %q", fm.ProgrammaticTools, c.Programmatic)
			}
		})
	}
}

// TestRenderReportsAPatternWhole is the fixture's grammar through the public
// Render: the envelope `nightgauge skill render --json` prints, and the
// RunOptions.AllowedTools every Go dispatcher builds from it, carry a
// `Tool(pattern)` entry whole.
func TestRenderReportsAPatternWhole(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev",
		"---\nname: s\nallowed-tools: Read, Bash(gh *), AskUserQuestion\n---\n\n# Body\n")
	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})

	if want := []string{"Read", "Bash(gh *)", "AskUserQuestion"}; !slices.Equal(res.AllowedTools, want) {
		t.Errorf("AllowedTools = %q, want %q", res.AllowedTools, want)
	}
	if want := []string{"Read", "Bash(gh *)"}; !slices.Equal(FilterHeadlessTools(res.AllowedTools), want) {
		t.Errorf("headless AllowedTools = %q, want %q", FilterHeadlessTools(res.AllowedTools), want)
	}
}
