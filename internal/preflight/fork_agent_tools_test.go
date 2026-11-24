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

// validateContextWrite matches feature-validate's executable context write,
// not the prose that names the command.
var validateContextWrite = regexp.MustCompile(`\| "\$\{NIGHTGAUGE_BIN:-nightgauge\}" layout write pipeline "validate-`)

// TestFeatureValidateRunsInlineWithBash locks #2851: feature-validate runs
// inline (no fork into a read-only agent) with Bash and Write allowed (AC 2),
// and its context-and-board include still writes validate-{N}.json through
// `nightgauge layout write pipeline` (AC 3).
func TestFeatureValidateRunsInlineWithBash(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{
		"skills/nightgauge-feature-validate",
		"claude-plugins/nightgauge/skills/feature-validate",
	} {
		rel := filepath.Join(dir, "SKILL.md")
		fields, _ := frontmatter(t, filepath.Join(root, rel))
		if fields["context"] == "fork" || fields["agent"] != "" {
			t.Errorf("%s: context=%q agent=%q; #2851 AC 2 requires feature-validate to run inline",
				rel, fields["context"], fields["agent"])
		}
		tools := strings.Fields(fields["allowed-tools"])
		for _, want := range []string{"Bash", "Write"} {
			if !hasTool(tools, want) {
				t.Errorf("%s: allowed-tools %q lacks %s; #2851 AC 2 needs it to write validate-{N}.json",
					rel, fields["allowed-tools"], want)
			}
		}
		include := filepath.Join(dir, "_includes", "context-and-board.md")
		raw, err := os.ReadFile(filepath.Join(root, include))
		if err != nil {
			t.Fatalf("read %s: %v", include, err)
		}
		if !validateContextWrite.Match(raw) {
			t.Errorf("%s: no `| nightgauge layout write pipeline \"validate-` command; "+
				"#2851 AC 3 expects the stage to write its context file", include)
		}
	}
}

// nightgaugeWriteCommands are nightgauge invocations that write state. The
// `layout write` form follows internal/hooks/gate_test.go
// TestGateAllowsPipelineContextWriteOutsidePipeline.
var nightgaugeWriteCommands = []string{
	"nightgauge layout write pipeline planning-1.json",
	"nightgauge layout write plans 1-x.md --from /tmp/p.md",
	"nightgauge project move-status 1 done",
	"nightgauge knowledge stamp f.md",
	"nightgauge finding record --summary x",
	"nightgauge discovered-work record --issue 1",
	"nightgauge forge issue comment 1 --body x",
	"nightgauge forge issue edit 1 --title x",
	"nightgauge forge issue close 1",
	"nightgauge pr merge 1",
	"nightgauge hook post-merge --issue 1",
}

// testRunnerReadGrants are the read-only nightgauge grants test-runner keeps.
var testRunnerReadGrants = []string{
	"Bash(nightgauge layout path *)",
	"Bash(nightgauge forge issue view *)",
}

// grantAdmits reports whether a Claude Code Bash grant admits cmd: bare
// `Bash` admits everything, a trailing `:*` is a prefix wildcard, and each
// `*` in the pattern matches any run of characters.
func grantAdmits(tool, cmd string) bool {
	if tool == "Bash" {
		return true
	}
	if !strings.HasPrefix(tool, "Bash(") || !strings.HasSuffix(tool, ")") {
		return false
	}
	pattern := tool[len("Bash(") : len(tool)-1]
	if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
		pattern = prefix + "*"
	}
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(cmd)
}

// TestReadOnlyAgentNightgaugeGrantsAreReadOnly locks #3578: the read-only
// test-runner agent may run only nightgauge verbs that cannot write.
func TestReadOnlyAgentNightgaugeGrantsAreReadOnly(t *testing.T) {
	const writeCmd = "nightgauge layout write pipeline planning-1.json"
	for _, wide := range []string{nightgaugeTool, "Bash", "Bash(*)", "Bash(nightgauge:*)"} {
		if !grantAdmits(wide, writeCmd) {
			t.Fatalf("grantAdmits(%q, %q) = false; the matcher cannot see a write grant", wide, writeCmd)
		}
	}
	for _, grant := range testRunnerReadGrants {
		if grantAdmits(grant, writeCmd) {
			t.Fatalf("grantAdmits(%q, %q) = true; the matcher over-matches", grant, writeCmd)
		}
	}
	root := repoRoot(t)
	const agent = "test-runner"
	tools := agentTools(t, root, agent)
	for _, tool := range tools {
		for _, cmd := range nightgaugeWriteCommands {
			if grantAdmits(tool, cmd) {
				t.Errorf(".claude/agents/%s.md grant %s admits write command %q", agent, tool, cmd)
			}
		}
	}
	for _, want := range testRunnerReadGrants {
		if !hasTool(tools, want) {
			t.Errorf(".claude/agents/%s.md tools: lacks %s", agent, want)
		}
	}
}
