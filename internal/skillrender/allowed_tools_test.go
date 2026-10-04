package skillrender

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		// Refused is a skill whose allowed-tools field is there but lists no
		// tool, which splitFrontmatter refuses (errNoAllowedTools).
		Refused bool `json:"refused"`
		// HeadlessRefused is a skill that declares tools but none a headless
		// run can use, which FilterHeadlessTools refuses (#2390).
		HeadlessRefused bool `json:"headless_refused"`
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
			_, fm, err := splitFrontmatter(c.Skill)
			if c.Refused {
				if !errors.Is(err, errNoAllowedTools) {
					t.Fatalf("err = %v, want errNoAllowedTools (declared %q)", err, fm.AllowedTools)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the skill read", err)
			}
			// nil and empty are the same answer here: the skill declares none.
			if !slices.Equal(fm.AllowedTools, c.Declared) {
				t.Errorf("declared = %q, want %q", fm.AllowedTools, c.Declared)
			}
			got, err := FilterHeadlessTools(fm.AllowedTools)
			if c.HeadlessRefused {
				if !errors.Is(err, ErrNoHeadlessTools) {
					t.Errorf("headless = %q, %v; want ErrNoHeadlessTools", got, err)
				}
			} else if err != nil || !slices.Equal(got, c.Headless) {
				t.Errorf("headless = %q, %v; want %q", got, err, c.Headless)
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
	headless := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}, Headless: true})
	if want := []string{"Read", "Bash(gh *)"}; !slices.Equal(headless.AllowedTools, want) {
		t.Errorf("headless AllowedTools = %q, want %q", headless.AllowedTools, want)
	}
}

// TestRenderRefusesAnEmptyAllowedTools is the refusal through the public
// Render, whichever file the tools come from: the base SKILL.md, the base one a
// compact profile reads its tools from, or a whole-file override. A skill whose
// allowed-tools lists no tool used to render as one that declares none, which
// every runner grants its default (#2358).
func TestRenderRefusesAnEmptyAllowedTools(t *testing.T) {
	const empty = "---\nname: s\nallowed-tools: []\n---\n\n# Body\n"

	t.Run("base", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "nightgauge-feature-dev", empty)
		_, err := Render(Options{Stage: "feature-dev", SkillsRoots: []string{root}})
		if !errors.Is(err, errNoAllowedTools) {
			t.Fatalf("err = %v, want errNoAllowedTools", err)
		}
		if want := filepath.Join("nightgauge-feature-dev", "SKILL.md"); !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %s", err, want)
		}
	})

	t.Run("compact profile", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "nightgauge-feature-dev", empty)
		profiles := filepath.Join(root, "nightgauge-feature-dev", profilesDir)
		if err := os.MkdirAll(profiles, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profiles, ProfileCompact+".md"), []byte("# Compact\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Render(Options{Stage: "feature-dev", SkillsRoots: []string{root}, Profile: ProfileCompact})
		if !errors.Is(err, errNoAllowedTools) {
			t.Fatalf("err = %v, want errNoAllowedTools", err)
		}
	})

	t.Run("whole-file override", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "nightgauge-feature-dev", "---\nname: s\nallowed-tools: Read\n---\n\n# Body\n")
		override := filepath.Join(root, "nightgauge-feature-dev", "_overlays", "claude-opus-5.SKILL.md")
		write(t, override, "---\nname: overridden\nallowed-tools:\n---\n\n# Override\n")
		_, err := Render(Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
		if !errors.Is(err, errNoAllowedTools) {
			t.Fatalf("err = %v, want errNoAllowedTools", err)
		}
		if !strings.Contains(err.Error(), override) {
			t.Errorf("err = %q, want it to name %s", err, override)
		}
	})
}
