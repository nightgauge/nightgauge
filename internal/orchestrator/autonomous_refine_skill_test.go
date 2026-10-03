package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// refineSkillFixture writes a minimal but real SKILL.md into a bundle-shaped
// root: <bundle>/nightgauge-issue-refine/SKILL.md. It is deliberately NOT
// written under the workspace root, because the whole defect is that the
// workspace root was the only place refinement ever looked.
func refineSkillFixture(t *testing.T) string {
	t.Helper()
	bundle := t.TempDir()
	dir := filepath.Join(bundle, "nightgauge-issue-refine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `---
name: nightgauge-issue-refine
description: Refine an issue into structured acceptance criteria.
allowed-tools: Read Glob Grep Bash AskUserQuestion
---

# Issue Refinement

Rewrite the issue body with structured acceptance criteria.
`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return bundle
}

// withRefineRoots points the refinement skill search at roots the test owns.
// skillrender.DefaultRoots derives the bundle root from os.Executable(), which
// under `go test` is the test binary in a temp dir — so without this seam the
// bundle half of the search is untestable.
func withRefineRoots(t *testing.T, ws, bundle string) {
	t.Helper()
	old := refineSkillRoots
	refineSkillRoots = func(string) []string {
		return []string{filepath.Join(ws, "skills"), bundle}
	}
	t.Cleanup(func() { refineSkillRoots = old })
}

// TestRefineViaCLIResolvesTheSkillOutsideTheWorkspace is the wiring guard for
// #1029. The workspace deliberately contains NO skills/ directory — exactly the
// shape of every repository that is not the nightgauge source tree.
//
// Before the fix, refineViaCLI joined <workspaceRoot>/skills/... and returned
// "refinement skill not found" from an os.Stat guard, never consulting the
// bundle root. The assertion is therefore not "it succeeded" (it cannot: there
// is no adapter) but "it got PAST location and reached dispatch".
func TestRefineViaCLIResolvesTheSkillOutsideTheWorkspace(t *testing.T) {
	ws := t.TempDir() // no skills/ directory, on purpose
	bundle := refineSkillFixture(t)
	withRefineRoots(t, ws, bundle)

	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws})
	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)

	err := as.refineViaCLI(context.Background(), "acme", "widgets", 42)
	if err == nil {
		t.Fatal("expected the nil-adapter refusal from execution.Manager, got nil")
	}
	// Location failures — the pre-#1029 behaviour. Either string means the
	// skill was never found, which is the defect.
	if strings.Contains(err.Error(), "refinement skill not found") ||
		strings.Contains(err.Error(), "SKILL.md not found for stage") {
		t.Fatalf("skill was not located off the bundle root: %v", err)
	}
	// Reaching the adapter check proves location AND composition succeeded.
	if !strings.Contains(err.Error(), "no skill runner adapter configured") {
		t.Fatalf("expected to reach RunStage's adapter check, got: %v", err)
	}
}

// TestRefineStageOptionsCarryAPromptAndToolAllowlist pins the half a
// roots-only fix would silently leave broken: execution.Manager pipes Prompt on
// stdin and passes AllowedTools to the adapter, so a StageOptions with an empty
// Prompt spawns a process that reads nothing and does nothing. That failure is
// strictly harder to detect than "skill not found", because it exits 0.
func TestRefineStageOptionsCarryAPromptAndToolAllowlist(t *testing.T) {
	ws := t.TempDir()
	bundle := refineSkillFixture(t)
	withRefineRoots(t, ws, bundle)

	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws})
	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)

	opts, err := as.refineStageOptions("acme", "widgets", 42, "claude")
	if err != nil {
		t.Fatalf("refineStageOptions: %v", err)
	}

	if !strings.HasPrefix(opts.SkillPath, bundle) {
		t.Errorf("SkillPath = %q, want it under the bundle root %q", opts.SkillPath, bundle)
	}
	if opts.Prompt == "" {
		t.Error("Prompt is empty — the resolved skill would never be read by any adapter")
	}
	if !strings.Contains(opts.Prompt, "Issue Refinement") {
		t.Errorf("Prompt does not carry the rendered skill body: %q", truncate(opts.Prompt, 200))
	}
	if !strings.Contains(opts.Prompt, "#42") {
		t.Errorf("Prompt does not carry the issue number: %q", truncate(opts.Prompt, 200))
	}
	if len(opts.AllowedTools) == 0 {
		t.Fatal("AllowedTools is empty — the adapter would run with no tool grant")
	}
	if !containsStr(opts.AllowedTools, "Bash") {
		t.Errorf("AllowedTools = %v, want it to carry Bash from the frontmatter", opts.AllowedTools)
	}
	// FilterHeadlessTools must strip the interactive tool: a headless
	// refinement that asks a question hangs until the stage timeout.
	if containsStr(opts.AllowedTools, "AskUserQuestion") {
		t.Errorf("AllowedTools = %v, want AskUserQuestion filtered for headless dispatch", opts.AllowedTools)
	}
	if opts.Stage != "issue-refine" {
		t.Errorf("Stage = %q, want issue-refine", opts.Stage)
	}
	if opts.TargetRepo != "acme/widgets" {
		t.Errorf("TargetRepo = %q, want acme/widgets", opts.TargetRepo)
	}
}

// TestRefineStageOptionsSetsCostBudget pins #1749: refineStageOptions is one
// of the two production StageOptions builders that must carry a non-zero
// CostBudget so the OpenCode watchdog / --max-budget-usd chain downstream
// (execution.Manager.buildRunOptions) is ever actually reached.
func TestRefineStageOptionsSetsCostBudget(t *testing.T) {
	ws := t.TempDir()
	bundle := refineSkillFixture(t)
	withRefineRoots(t, ws, bundle)

	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws})
	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)

	opts, err := as.refineStageOptions("acme", "widgets", 42, "claude")
	if err != nil {
		t.Fatalf("refineStageOptions: %v", err)
	}

	want := PipelineBudgetCeilingUSD(ws)
	if want <= 0 {
		t.Fatalf("PipelineBudgetCeilingUSD(%q) = %v, want > 0", ws, want)
	}
	if opts.CostBudget != want {
		t.Errorf("CostBudget = %v, want %v (PipelineBudgetCeilingUSD(workspaceRoot))", opts.CostBudget, want)
	}
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestRefineStageOptionsModelFollowsThePerformanceMode pins #2384. Refinement
// used to dispatch the literal "sonnet" in every mode, below maximum's
// [opus, opus] envelope and above a haiku max_model. It now resolves through
// the per-stage chain: no mode pins issue-refine, so maximum's envelope raises
// the Sonnet base, max_model caps it, and an explicit per-stage model wins.
func TestRefineStageOptionsModelFollowsThePerformanceMode(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		config string // workspace config body; "" = no config file
		env    string // NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_REFINE
		want   string
	}{
		{"elevated keeps the sonnet base", "elevated", "", "", "sonnet"},
		{"efficiency keeps the sonnet base", "efficiency", "", "", "sonnet"},
		{"frontier keeps the sonnet base", "frontier", "", "", "sonnet"},
		{"maximum raises it to its opus floor", "maximum", "", "", "opus"},
		{"max_model caps it", "elevated", "model_routing:\n  max_model: haiku\n", "", "haiku"},
		{"the stage's environment override wins over the mode", "maximum", "", "haiku", "haiku"},
		{"pipeline.stage_models wins over the mode",
			"maximum", "model_routing:\n  mode: hybrid\npipeline:\n  stage_models:\n    issue-refine: haiku\n", "", "haiku"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := isolatedWorkspace(t)
			if c.config != "" {
				ws = routedWorkspace(t, c.config)
			}
			t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", c.mode)
			t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_ISSUE_REFINE", c.env)
			withRefineRoots(t, ws, refineSkillFixture(t))

			sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: ws})
			as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{}, nil, DefaultAutonomousConfig(), ws)
			opts, err := as.refineStageOptions("acme", "widgets", 42, "claude")
			if err != nil {
				t.Fatalf("refineStageOptions: %v", err)
			}
			if opts.Model != c.want {
				t.Errorf("refinement model = %q, want %q", opts.Model, c.want)
			}
		})
	}
}
