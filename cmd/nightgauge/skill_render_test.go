package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `skill render`'s CLI surface. The composition rules themselves are covered
// in internal/skillrender; what these pin is the contract the extension's
// skillRunner depends on since #79 — one spawn returning both the composed
// body and the frontmatter tool lists.

func writeStageSkill(t *testing.T, root, stage, body string) {
	t.Helper()
	dir := filepath.Join(root, "nightgauge-"+stage)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func runRender(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := skillRenderCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

const toolSkill = `---
name: test-skill
allowed-tools: Read Edit Bash
programmatic-tools: TodoWrite
mcp-tools: all
---

# Test Skill

Do the thing.
`

func TestRenderJSONIncludeContentCarriesBodyAndTools(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t,
		"--stage", "feature-dev", "--skills-root", root, "--json", "--include-content")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}

	var env struct {
		Content           string   `json:"content"`
		SkillPath         string   `json:"skill_path"`
		AllowedTools      []string `json:"allowed_tools"`
		ProgrammaticTools []string `json:"programmatic_tools"`
		MCPTools          []string `json:"mcp_tools"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}

	// The body is the whole point of the flag: without it a host that also
	// needs the tool lists has to render a second time, against a filesystem
	// that may have changed in between.
	if !strings.Contains(env.Content, "# Test Skill") {
		t.Errorf("content missing skill body:\n%s", env.Content)
	}
	if strings.Contains(env.Content, "name: test-skill") {
		t.Error("content should have frontmatter stripped")
	}
	if env.SkillPath == "" || !filepath.IsAbs(env.SkillPath) {
		t.Errorf("skill_path = %q, want an absolute path", env.SkillPath)
	}
	if got, want := strings.Join(env.AllowedTools, ","), "Read,Edit,Bash"; got != want {
		t.Errorf("allowed_tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(env.ProgrammaticTools, ","), "TodoWrite"; got != want {
		t.Errorf("programmatic_tools = %q, want %q", got, want)
	}
	if got, want := strings.Join(env.MCPTools, ","), "all"; got != want {
		t.Errorf("mcp_tools = %q, want %q", got, want)
	}
}

func TestRenderJSONOmitsContentByDefault(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}

	// The default envelope stays readable — the body goes to stdout on the
	// non-JSON path. A `content` key appearing here would mean the flag had
	// stopped gating anything, which is the way this test can actually fail.
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}
	if _, present := raw["content"]; present {
		t.Errorf("plain --json must not carry content:\n%s", out)
	}
	if _, present := raw["skill_path"]; !present {
		t.Errorf("plain --json should still carry provenance:\n%s", out)
	}
}

func TestRenderIncludeContentRequiresJSON(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--include-content")
	if err == nil {
		t.Fatalf("--include-content without --json must refuse, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "requires --json") {
		t.Errorf("error should name the missing flag, got %v", err)
	}
}

func TestRenderIncludeContentAgreesWithPlainStdout(t *testing.T) {
	// The root must literally end in `skills` — that is the production layout
	// (<repo>/skills/nightgauge-<stage>/), and it is load-bearing here. The
	// rewrite turns `skills/_shared/` into `<root>/_shared/`; only when <root>
	// itself contains a `skills/` segment does the result STILL contain the
	// needle, which is what makes a second pass destructive rather than inert.
	// A bare t.TempDir() root hides that entirely: the doubling mutation
	// survives against it, because there is nothing left for pass two to match.
	root := filepath.Join(t.TempDir(), "skills")
	shared := filepath.Join(root, "_shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shared, "PIPELINE_CONTEXT.md"),
		[]byte("## System Context\n"), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	writeStageSkill(t, root, "feature-dev", `---
name: test-skill
allowed-tools: Read
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->

Read `+"`skills/_shared/PIPELINE_CONTEXT.md`"+` for context.
`)

	plain, err := runRender(t, "--stage", "feature-dev", "--skills-root", root)
	if err != nil {
		t.Fatalf("plain execute: %v\n%s", err, plain)
	}
	enveloped, err := runRender(t,
		"--stage", "feature-dev", "--skills-root", root, "--json", "--include-content")
	if err != nil {
		t.Fatalf("json execute: %v\n%s", err, enveloped)
	}
	var env struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(enveloped), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}

	// Byte-equality, not "both contain the heading": the flag must be a
	// transport change and nothing else. An extra trailing newline on one
	// branch would diverge here and nowhere else.
	if env.Content != plain {
		t.Errorf("envelope content differs from stdout render\n--- stdout ---\n%q\n--- envelope ---\n%q",
			plain, env.Content)
	}

	// Both renders must have rewritten the directive EXACTLY once. Comparing
	// the two against each other cannot see a double rewrite — both call the
	// same Render — so assert the absolute form directly against the root.
	want := filepath.Join(root, "_shared") + string(filepath.Separator) + "PIPELINE_CONTEXT.md"
	if !strings.Contains(env.Content, want) {
		t.Errorf("rewritten path %q missing from render:\n%s", want, env.Content)
	}

	// Note what is deliberately NOT asserted: that `skills/_shared/` is gone.
	// It is still there, INSIDE the absolute path, and that is the whole reason
	// the rewrite is not idempotent — pass two would match it again and produce
	// `<dir>//<root>/_shared/…`, a path that resolves to nothing. The doubled
	// separator is that corruption's signature.
	if strings.Contains(env.Content, string(filepath.Separator)+string(filepath.Separator)) {
		t.Errorf("doubled separator — the path was rewritten twice:\n%s", env.Content)
	}
}

// ─── --context-window (#1645, ADR 023) ───────────────────────────────────────

// writeBigStageSkill writes a skill padded well past any plausible small test
// budget, so a tiny --context-window can be made to fail without a real
// skills/ checkout.
func writeBigStageSkill(t *testing.T, root, stage string, repeats int) {
	t.Helper()
	var body strings.Builder
	body.WriteString("---\nname: big-skill\nallowed-tools: Read\n---\n\n# Big Skill\n\n")
	for i := 0; i < repeats; i++ {
		body.WriteString("This line pads the rendered content out to a measurable token count.\n")
	}
	writeStageSkill(t, root, stage, body.String())
}

func TestRenderNoFlagOutputByteIdenticalWithAndWithoutContextWindowSupport(t *testing.T) {
	// The no-flag path must be untouched by the existence of --context-window:
	// same bytes on stdout, no stderr verdict line, exit 0.
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root)
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# Test Skill") {
		t.Errorf("missing skill body:\n%s", out)
	}
}

func TestRenderContextWindowOverBudgetExitsNonZero(t *testing.T) {
	root := t.TempDir()
	writeBigStageSkill(t, root, "feature-dev", 5000)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--context-window", "1000")
	if err == nil {
		t.Fatalf("expected a non-zero exit for an over-budget render, got success:\n%s", out)
	}
	if !strings.Contains(out, "# Big Skill") {
		t.Errorf("content should still be printed to stdout on a failing verdict:\n%s", out)
	}
}

func TestRenderContextWindowUnderBudgetExitsZero(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--context-window", "1000000")
	if err != nil {
		t.Fatalf("expected success for an under-budget render: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# Test Skill") {
		t.Errorf("missing skill body:\n%s", out)
	}
}

func TestRenderContextWindowJSONCarriesBudgetVerdict(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--json", "--context-window", "1000000")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	var env struct {
		Budget *struct {
			Fits            bool
			EstimatedTokens int
			Budget          int
			Window          int
			Share           float64
		} `json:"budget"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}
	if env.Budget == nil {
		t.Fatalf("expected a budget verdict in the envelope:\n%s", out)
	}
	if !env.Budget.Fits {
		t.Errorf("expected Fits=true for a tiny render against a 1M window: %+v", env.Budget)
	}
	if env.Budget.Window != 1_000_000 {
		t.Errorf("Window = %d, want 1000000", env.Budget.Window)
	}
}

func TestRenderContextWindowOmittedFromJSONWhenUnset(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}
	if _, present := raw["budget"]; present {
		t.Errorf("plain --json without --context-window must not carry a budget verdict:\n%s", out)
	}
}

// ─── #1654: --profile ───────────────────────────────────────────────────────

func TestRenderProfileCompactFallsBackToFullWithoutFlag(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	noFlag, err := runRender(t, "--stage", "feature-dev", "--skills-root", root)
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, noFlag)
	}
	full, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--profile", "full")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, full)
	}
	if noFlag != full {
		t.Errorf("no-flag and --profile full must render byte-identically:\nno-flag: %q\nfull:    %q", noFlag, full)
	}

	// No _profiles/compact.md exists for this fixture skill, so --profile
	// compact must fall back to the same full render, not error.
	compact, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--profile", "compact")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, compact)
	}
	if compact != full {
		t.Errorf("--profile compact with no compact profile on disk must fall back to full byte-for-byte:\ncompact: %q\nfull:    %q", compact, full)
	}
}

func TestRenderProfileCompactComposesFromProfileFile(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)
	compactDir := filepath.Join(root, "nightgauge-feature-dev", "_profiles")
	if err := os.MkdirAll(compactDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(compactDir, "compact.md"), []byte("# Compact Skeleton\n"), 0o644); err != nil {
		t.Fatalf("write compact profile: %v", err)
	}

	out, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--profile", "compact", "--json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	var env struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}
	if env.Profile != "compact" {
		t.Errorf(`envelope "profile" = %q, want "compact"`, env.Profile)
	}

	plain, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--profile", "compact")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, plain)
	}
	if !strings.Contains(plain, "Compact Skeleton") {
		t.Errorf("expected the compact profile's own content, got:\n%s", plain)
	}
	if strings.Contains(plain, "Do the thing.") {
		t.Errorf("compact render should not carry the full base body:\n%s", plain)
	}
}

func TestRenderProfileRejectsUnknownValue(t *testing.T) {
	root := t.TempDir()
	writeStageSkill(t, root, "feature-dev", toolSkill)

	_, err := runRender(t, "--stage", "feature-dev", "--skills-root", root, "--profile", "bogus")
	if err == nil {
		t.Fatal("expected an error for an unrecognized --profile value, got success")
	}
}
