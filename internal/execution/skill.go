// skill.go provides SKILL.md reading, frontmatter parsing, include expansion,
// and prompt building — matching the TypeScript skillRunner.ts behavior.
package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/state"
)

// StageSkillDirs maps pipeline stages to their skill directory names.
// Matches the TypeScript STAGE_TO_SKILL_DIR in skillRunner.ts.
var StageSkillDirs = map[state.PipelineStage]string{
	state.StageIssuePickup:     "nightgauge-issue-pickup",
	state.StageFeaturePlanning: "nightgauge-feature-planning",
	state.StageFeatureDev:      "nightgauge-feature-dev",
	state.StageFeatureValidate: "nightgauge-feature-validate",
	state.StagePRCreate:        "nightgauge-pr-create",
	state.StagePRMerge:         "nightgauge-pr-merge",
}

// SkillData holds parsed SKILL.md content and frontmatter.
type SkillData struct {
	Name              string
	AllowedTools      []string
	ProgrammaticTools []string
	MCPTools          []string
	Content           string // Full SKILL.md content (after include expansion)
	Path              string // Absolute path to the SKILL.md file
}

// FindSkillFile locates the SKILL.md file for a pipeline stage.
func FindSkillFile(workspaceRoot string, stage state.PipelineStage) (string, error) {
	skillDir, ok := StageSkillDirs[stage]
	if !ok || skillDir == "" {
		return "", fmt.Errorf("no skill directory for stage %q", stage)
	}

	// Primary location: skills/<dir>/SKILL.md
	skillPath := filepath.Join(workspaceRoot, "skills", skillDir, "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		return skillPath, nil
	}

	// Fallback: claude-plugins/nightgauge/commands/<dir>.md
	fallbackPath := filepath.Join(workspaceRoot, "claude-plugins", "nightgauge", "commands", skillDir+".md")
	if _, err := os.Stat(fallbackPath); err == nil {
		return fallbackPath, nil
	}

	return "", fmt.Errorf("SKILL.md not found for stage %q (tried %s)", stage, skillPath)
}

// ReadSkillFile reads and parses a SKILL.md file, expanding includes.
func ReadSkillFile(skillPath string) (*SkillData, error) {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("read skill: %w", err)
	}

	content := string(raw)
	skillDir := filepath.Dir(skillPath)

	// Parse YAML frontmatter (between --- delimiters)
	data := &SkillData{Path: skillPath}
	if strings.HasPrefix(content, "---\n") {
		endIdx := strings.Index(content[4:], "\n---")
		if endIdx >= 0 {
			frontmatter := content[4 : 4+endIdx]
			content = content[4+endIdx+4:] // Skip past closing ---\n

			data.Name = extractYAMLField(frontmatter, "name")
			data.AllowedTools = splitTools(extractYAMLField(frontmatter, "allowed-tools"))
			data.ProgrammaticTools = splitTools(extractYAMLField(frontmatter, "programmatic-tools"))
			data.MCPTools = splitTools(extractYAMLField(frontmatter, "mcp-tools"))
		}
	}

	// Expand <!-- include: path --> directives
	content = expandIncludes(content, skillDir)

	data.Content = content
	return data, nil
}

// BuildPrompt constructs the prompt to pass to the AI agent via stdin.
// Matches the TypeScript buildStagePrompt() behavior.
//
// Stable-prefix-first ordering (#3805): the skill body is byte-identical across
// issues for a given stage, so it is written first to form the cacheable
// prefix; the variable invocation context block trails it. This mirrors the TS
// builder's logical ordering (stable body, "---", variable trailer) — the two
// builders are aligned on ordering, not byte-identical strings (ADR-001).
//
// skillDir (when non-empty) is the absolute directory of the resolved
// SKILL.md: skill-relative read directives are rewritten against it so they
// resolve from cross-repo worktrees (#196). The rewrite is host-constant per
// stage, so the body stays byte-identical across issues (cache-safe).
func BuildPrompt(stage state.PipelineStage, skillContent string, issueNumber int, skillDir string) string {
	var sb strings.Builder

	// Stable skill body first — forms the cacheable prefix (#3805).
	if skillDir != "" {
		sb.WriteString(RewriteSkillRelativePaths(skillContent, stage, skillDir))
	} else {
		sb.WriteString(skillContent)
	}
	sb.WriteString("\n\n---\n\n")

	// Variable invocation context last (matches TS headless mode injection).
	sb.WriteString("## Invocation Context\n\n")
	sb.WriteString("- **Mode**: headless (non-interactive pipeline execution)\n")
	sb.WriteString(fmt.Sprintf("- **Issue**: #%d\n", issueNumber))
	sb.WriteString(fmt.Sprintf("- **Stage**: %s\n", stage))
	if skillDir != "" {
		sb.WriteString(fmt.Sprintf("- **Skill directory**: %s — supporting files (_includes/, _shared/) live here, NOT under the current working directory; never scan the filesystem for them (#196)\n", skillDir))
	}
	sb.WriteString("- **AskUserQuestion**: DISABLED — fail fast if undecidable\n")
	sb.WriteString("- **Auto-accept**: All tool calls are auto-approved\n")

	return sb.String()
}

// RewriteSkillRelativePaths rewrites skill-relative read-directive paths
// ("Read `skills/<name>/_includes/foo.md` now…") to absolute host paths.
// ADR-010 assumed CWD is the nightgauge repo root — only true when
// dogfooding nightgauge itself; cross-repo runs spawn in the target repo's
// worktree, which has no skills/ directory, and agents fell back to
// whole-filesystem scans and stale ~/.codex/skills copies (#196). Only the
// skill's OWN references are rewritten (resolved dir basename, canonical
// nightgauge-<stage> name, and the prefix-stripped <stage> variant the
// plugin cache uses) plus the sibling skills/_shared/ — cross-skill
// references keep naming the other skill.
func RewriteSkillRelativePaths(content string, stage state.PipelineStage, skillDir string) string {
	dir := strings.TrimRight(skillDir, "/\\")
	shared := filepath.Join(filepath.Dir(dir), "_shared") + string(filepath.Separator)
	out := strings.ReplaceAll(content, "skills/_shared/", shared)
	names := map[string]bool{
		filepath.Base(dir):            true,
		"nightgauge-" + string(stage): true,
		string(stage):                 true,
	}
	for name := range names {
		if name == "" {
			continue
		}
		out = strings.ReplaceAll(out, "skills/"+name+"/", dir+string(filepath.Separator))
	}
	return out
}

// expandIncludes replaces <!-- include: path --> directives with file content.
// Resolves paths relative to the SKILL.md file's directory.
var includePattern = regexp.MustCompile(`<!-- include: (.+?) -->`)

func expandIncludes(content string, skillDir string) string {
	return includePattern.ReplaceAllStringFunc(content, func(match string) string {
		subs := includePattern.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		relativePath := strings.TrimSpace(subs[1])
		absPath := filepath.Join(skillDir, relativePath)

		data, err := os.ReadFile(absPath)
		if err != nil {
			return match // Leave directive as-is if file not found (portability)
		}
		return string(data)
	})
}

// extractYAMLField does simple line-based extraction of a top-level YAML field.
// Handles both single-line (key: value) and multi-word values.
func extractYAMLField(frontmatter string, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			// Strip surrounding quotes
			value = strings.Trim(value, "\"'")
			return value
		}
	}
	return ""
}

// splitTools splits a space-separated tool list into a slice.
// Filters out AskUserQuestion since it doesn't work in headless mode.
func splitTools(tools string) []string {
	if tools == "" {
		return nil
	}
	var result []string
	for _, t := range strings.Fields(tools) {
		if t != "AskUserQuestion" {
			result = append(result, t)
		}
	}
	return result
}
