package preflight

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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

// nightgaugeInvocation captures the verb tokens of a nightgauge call written
// as `"${NIGHTGAUGE_BIN:-nightgauge}" <verb…>`, `"$BINARY" <verb…>` or a bare
// `nightgauge <verb…>`.
var nightgaugeInvocation = regexp.MustCompile(
	`(?:"\$\{NIGHTGAUGE_BIN:-nightgauge\}"|"?\$BINARY"?|\bnightgauge)((?:[ \t]+[a-z][a-z-]*)+)`)

// forkNightgaugeViolations lists the nightgauge invocations in body that no
// tool in tools admits. Each is normalised to `nightgauge <verbs…> ` so a
// `Bash(nightgauge <verb> *)` grant can match it.
func forkNightgaugeViolations(body string, tools []string) []string {
	var violations []string
	seen := map[string]bool{}
	for _, m := range nightgaugeInvocation.FindAllStringSubmatch(body, -1) {
		cmd := "nightgauge " + strings.Join(strings.Fields(m[1]), " ") + " "
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		admitted := false
		for _, tool := range tools {
			if grantAdmits(tool, cmd) {
				admitted = true
				break
			}
		}
		if !admitted {
			violations = append(violations, strings.TrimSpace(cmd))
		}
	}
	return violations
}

// includeDirective matches a `<!-- include: path -->` line in a skill body.
var includeDirective = regexp.MustCompile(`<!-- include: (\S+) -->`)

// skillText returns body plus every file the skill pulls in: the
// `_includes/*.md` files beside its SKILL.md and each `<!-- include: -->`
// target. A forked skill runs all of that text under its agent's allowlist.
func skillText(t *testing.T, skillPath, body string) string {
	t.Helper()
	dir := filepath.Dir(skillPath)
	parts := []string{body}
	seen := map[string]bool{}
	add := func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read include %s of %s: %v", path, skillPath, err)
		}
		parts = append(parts, string(raw))
	}
	includes, err := filepath.Glob(filepath.Join(dir, "_includes", "*.md"))
	if err != nil {
		t.Fatalf("glob includes of %s: %v", skillPath, err)
	}
	for _, inc := range includes {
		add(inc)
	}
	for _, m := range includeDirective.FindAllStringSubmatch(body, -1) {
		add(filepath.Clean(filepath.Join(dir, m[1])))
	}
	return strings.Join(parts, "\n")
}

// TestForkedSkillAgentsAllowNightgauge fails when a skill that forks into a
// Claude Code agent calls a nightgauge verb the agent's allowlist denies
// (#3561, #3581).
func TestForkedSkillAgentsAllowNightgauge(t *testing.T) {
	researcher := readOnlyNightgaugeGrants
	for _, body := range []string{
		"X=$(\"${NIGHTGAUGE_BIN:-nightgauge}\" layout write pipeline x.json)",
		"$BINARY layout write pipeline x.json",
		"nightgauge layout write pipeline x.json",
	} {
		v := forkNightgaugeViolations(body, researcher)
		if len(v) != 1 || !strings.HasPrefix(v[0], "nightgauge layout write pipeline") {
			t.Fatalf("%q against read-only grants yielded %v; want one layout write violation", body, v)
		}
	}
	if v := forkNightgaugeViolations(
		"X=$(\"${NIGHTGAUGE_BIN:-nightgauge}\" layout path pipeline x.json)\n\"$BINARY\" forge issue view 1",
		researcher); len(v) != 0 {
		t.Fatalf("read-only calls yielded violations %v; want none", v)
	}
	root := repoRoot(t)
	// A write verb that appears only in an included file must still be seen.
	fixtureDir := t.TempDir()
	skillDir := filepath.Join(fixtureDir, "skill")
	for path, text := range map[string]string{
		filepath.Join(skillDir, "_includes", "ctx.md"):    "\"$BINARY\" layout write pipeline x.json\n",
		filepath.Join(fixtureDir, "_shared", "SHARED.md"): "nightgauge finding record --issue 1\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixtureBody := "<!-- include: ../_shared/SHARED.md -->\n"
	if v := forkNightgaugeViolations(
		skillText(t, filepath.Join(skillDir, "SKILL.md"), fixtureBody), researcher); len(v) != 2 {
		t.Fatalf("write verbs in included files yielded %v; want two violations", v)
	}
	var skills []string
	for _, pattern := range []string{"skills/*/SKILL.md", "claude-plugins/nightgauge/skills/*/SKILL.md"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		skills = append(skills, matches...)
	}
	if len(skills) == 0 {
		t.Fatal("no SKILL.md found; the scan is vacuous")
	}
	// Since #3581 no skill forks into an agent, so the scan is expected to
	// examine nothing; the fixtures above prove the check itself can go red.
	examined := 0
	for _, path := range skills {
		fields, body := frontmatter(t, path)
		agent := fields["agent"]
		if fields["context"] != "fork" || agent == "" {
			continue
		}
		examined++
		rel, _ := filepath.Rel(root, path)
		for _, cmd := range forkNightgaugeViolations(skillText(t, path, body), agentTools(t, root, agent)) {
			t.Errorf("%s forks into agent %q and calls %q, which .claude/agents/%s.md tools: "+
				"does not admit; run the skill inline instead", rel, agent, cmd, agent)
		}
	}
	t.Logf("examined %d forked skills with an agent", examined)
}

// TestResearcherSkillsRunInline locks #3581: skills that call state-writing
// nightgauge verbs run inline with Bash and Write instead of forking into
// the read-only pipeline-researcher agent.
func TestResearcherSkillsRunInline(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"issue-pickup", "feature-planning", "backlog-preflight", "health-check"} {
		for _, rel := range []string{
			filepath.Join("skills", "nightgauge-"+name, "SKILL.md"),
			filepath.Join("claude-plugins", "nightgauge", "skills", name, "SKILL.md"),
		} {
			fields, _ := frontmatter(t, filepath.Join(root, rel))
			if fields["context"] == "fork" || fields["agent"] != "" {
				t.Errorf("%s: context=%q agent=%q; #3581 requires it to run inline",
					rel, fields["context"], fields["agent"])
			}
			tools := strings.Fields(fields["allowed-tools"])
			for _, want := range []string{"Bash", "Write"} {
				if !hasTool(tools, want) {
					t.Errorf("%s: allowed-tools %q lacks %s", rel, fields["allowed-tools"], want)
				}
			}
		}
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

// readOnlyNightgaugeGrants are the read-only nightgauge grants test-runner and
// pipeline-researcher keep.
var readOnlyNightgaugeGrants = []string{
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

// TestReadOnlyAgentNightgaugeGrantsAreReadOnly locks #3578 and #3581: the
// read-only agents may run only nightgauge verbs that cannot write.
func TestReadOnlyAgentNightgaugeGrantsAreReadOnly(t *testing.T) {
	const writeCmd = "nightgauge layout write pipeline planning-1.json"
	for _, wide := range []string{nightgaugeTool, "Bash", "Bash(*)", "Bash(nightgauge:*)"} {
		if !grantAdmits(wide, writeCmd) {
			t.Fatalf("grantAdmits(%q, %q) = false; the matcher cannot see a write grant", wide, writeCmd)
		}
	}
	for _, grant := range readOnlyNightgaugeGrants {
		if grantAdmits(grant, writeCmd) {
			t.Fatalf("grantAdmits(%q, %q) = true; the matcher over-matches", grant, writeCmd)
		}
	}
	root := repoRoot(t)
	agents := map[string][]string{
		"test-runner":         readOnlyNightgaugeGrants,
		"pipeline-researcher": readOnlyNightgaugeGrants,
		"code-reviewer":       nil,
		"pipeline-optimizer":  nil,
	}
	for agent, required := range agents {
		tools := agentTools(t, root, agent)
		for _, tool := range tools {
			for _, cmd := range nightgaugeWriteCommands {
				if grantAdmits(tool, cmd) {
					t.Errorf(".claude/agents/%s.md grant %s admits write command %q", agent, tool, cmd)
				}
			}
			if len(required) == 0 && strings.HasPrefix(tool, "Bash(nightgauge") {
				t.Errorf(".claude/agents/%s.md grants %s; it needs no nightgauge verb", agent, tool)
			}
		}
		for _, want := range required {
			if !hasTool(tools, want) {
				t.Errorf(".claude/agents/%s.md tools: lacks %s", agent, want)
			}
		}
	}
}
