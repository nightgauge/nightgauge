package preflight

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// nightgaugeCall matches a skill body that invokes the nightgauge binary.
var nightgaugeCall = regexp.MustCompile(`\bnightgauge\s+[a-z]|NIGHTGAUGE_BIN`)

const nightgaugeTool = "Bash(nightgauge *)"

// frontmatter returns the key/value pairs of a file's leading --- block and
// the body that follows it.
func frontmatter(t *testing.T, path string) (map[string]string, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	fields := map[string]string{}
	if !strings.HasPrefix(text, "---\n") {
		return fields, text
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fields, text
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return fields, rest[end:]
}

func agentTools(t *testing.T, root, agent string) []string {
	t.Helper()
	path := filepath.Join(root, ".claude", "agents", agent+".md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("agent %s: %v", agent, err)
	}
	fields, _ := frontmatter(t, path)
	var tools []string
	for _, tool := range strings.Split(fields["tools"], ",") {
		if tool = strings.TrimSpace(tool); tool != "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

func hasTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

// TestForkedSkillAgentsAllowNightgauge fails when a skill that forks into a
// Claude Code agent calls nightgauge but the agent's allowlist denies it
// (#3561).
func TestForkedSkillAgentsAllowNightgauge(t *testing.T) {
	root := repoRoot(t)
	var skills []string
	for _, pattern := range []string{"skills/*/SKILL.md", "claude-plugins/nightgauge/skills/*/SKILL.md"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		skills = append(skills, matches...)
	}
	examined := 0
	for _, path := range skills {
		fields, body := frontmatter(t, path)
		agent := fields["agent"]
		if fields["context"] != "fork" || agent == "" || !nightgaugeCall.MatchString(body) {
			continue
		}
		examined++
		rel, _ := filepath.Rel(root, path)
		if !hasTool(agentTools(t, root, agent), nightgaugeTool) {
			t.Errorf("%s forks into agent %q and calls nightgauge, but "+
				".claude/agents/%s.md tools: lacks %s; add it to the allowlist",
				rel, agent, agent, nightgaugeTool)
		}
	}
	if examined == 0 {
		t.Fatal("no context: fork skill with an agent was examined; the scan is vacuous")
	}
}

// TestReadOnlyAgentsKeepReadOnlyTools pins the read-only tools the forked
// agents already had, so widening for nightgauge does not replace them.
func TestReadOnlyAgentsKeepReadOnlyTools(t *testing.T) {
	root := repoRoot(t)
	want := map[string][]string{
		"pipeline-researcher": {"Bash(gh *)", "Bash(git log *)", "Bash(git diff *)"},
		"pipeline-optimizer":  {"Bash(cat *)", "Bash(wc *)", "Bash(jq *)"},
	}
	for agent, required := range want {
		tools := agentTools(t, root, agent)
		for _, tool := range required {
			if !hasTool(tools, tool) {
				t.Errorf(".claude/agents/%s.md tools: lost %s", agent, tool)
			}
		}
	}
}
