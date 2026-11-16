package opencodeplugin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// budgetDriver imports the embedded gates.js and runs each call through
// enforceExplorationBudget, printing "ok" or the refusal per call.
const budgetDriver = `
const mod = await import(process.env.NG_GATES_PATH);
const calls = JSON.parse(process.env.NG_CALLS);
const ctx = { location: { directory: "/w/.nightgauge/worktrees/issue-1" } };
const out = calls.map(([tool, input]) => {
  try { mod.enforceExplorationBudget(ctx, { tool, sessionID: "s", input }); return "ok"; }
  catch (e) { return e.message; }
});
process.stdout.write(JSON.stringify(out));
`

// TestExplorationBudget2188 proves exploration calls past the budget are
// refused, skill, .nightgauge/ and per-clone state reads and writes (a
// .git/nightgauge/ path, `nightgauge layout path|write|append`, ADR-024 § 7)
// are not counted or refused, and a budget of 0 or unset is off (#2188).
func TestExplorationBudget2188(t *testing.T) {
	node := requireNode(t)
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	gates := filepath.Join(entry, "nightgauge", "gates.js")
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(budgetDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := [][]any{
		{"read", map[string]any{"path": "internal/a.go"}},                                             // 1
		{"read", map[string]any{"path": "skills/_shared/GOTCHAS.md"}},                                 // exempt
		{"read", map[string]any{"path": "/home/u/.claude/plugins/ng/skills/_includes/x.md"}},          // exempt
		{"read", map[string]any{"path": "skills/nightgauge-feature-planning/SKILL.md"}},               // exempt
		{"read", map[string]any{"path": "/w/.git/nightgauge/pipeline/ctx.json"}},                      // exempt
		{"write", map[string]any{"path": "notes.md"}},                                                 // not exploration
		{"shell", map[string]any{"command": "cat a.go > b.go"}},                                       // write
		{"shell", map[string]any{"command": "nightgauge layout write plans p.md --from p.md"}},        // write
		{"shell", map[string]any{"command": "nightgauge layout write plans p.md <<'EOF'\nplan\nEOF"}}, // heredoc write
		{"shell", map[string]any{"command": "echo \"=== phase ===\"; true"}},                          // marker
		{"shell", map[string]any{"command": "KB=$(jq -r .x cfg.json); echo \"$KB\" 2>/dev/null"}},     // 2
		{"shell", map[string]any{"command": "for f in internal/*.go; do head -3 $f; done"}},           // 3
		{"read", map[string]any{"path": "skills/nightgauge-dev/SKILL.md"}},                            // refused: source, not exempt
		{"shell", map[string]any{"command": "echo \"=== x ===\"; grep -n foo a.go"}},                  // refused
		{"read", map[string]any{"path": "skills/_shared/SELF_ASSESSMENT.md"}},                         // exempt still
		{"edit", map[string]any{"path": "a.go"}},                                                      // allowed
		{"shell", map[string]any{"command": "python3 -c 'print(1)' >/dev/null"}},                      // refused: /dev/null is not a write
		{"shell", map[string]any{"command": "cat \"$(nightgauge layout path pipeline ctx.json)\""}},   // exempt
		{"shell", map[string]any{"command": "printf 'x\\n' | nightgauge layout append logs a.log"}},   // write
		{"read", map[string]any{"path": "/w/.git/nightgauge/plans/p.md"}},                             // exempt
		{"read", map[string]any{"path": "/w/.git/config"}},                                            // refused: not per-clone state
	}
	raw, _ := json.Marshal(calls)
	run := func(budget string) []string {
		t.Helper()
		cmd := exec.Command(node, driver)
		cmd.Env = append(removeEnv(removeEnv(os.Environ(), EnvExplorationBudget), "NIGHTGAUGE_ISSUE_NUMBER"),
			"NG_GATES_PATH="+gates, "NG_CALLS="+string(raw), "NIGHTGAUGE_ISSUE_NUMBER=42")
		if budget != "" {
			cmd.Env = append(cmd.Env, EnvExplorationBudget+"="+budget)
		}
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("driver: %v\n%s", err, ee.Stderr)
			}
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("driver output %q: %v", out, err)
		}
		return got
	}

	got := run("3")
	refusal := "[nightgauge-gate:exploration-budget] exploration budget of 3 reads spent; " +
		"write the plan now with nightgauge layout write plans <name>, " +
		"then planning-42.json with nightgauge layout write pipeline"
	for i := range calls {
		want := "ok"
		if i == 12 || i == 13 || i == 16 || i == 20 {
			want = refusal
		}
		if got[i] != want {
			t.Errorf("call %d %v: got %q, want %q", i, calls[i], got[i], want)
		}
	}
	for _, budget := range []string{"", "0", "abc"} {
		for i, r := range run(budget) {
			if r != "ok" {
				t.Errorf("budget %q: call %d refused: %s", budget, i, r)
			}
		}
	}
}

// TestExplorationBudgetExemptsKnowledgeBase2193 proves knowledge-base reads
// outside the worktree (the main checkout's .nightgauge/knowledge, run 18)
// and under the configured knowledge directory are never counted or refused.
func TestExplorationBudgetExemptsKnowledgeBase2193(t *testing.T) {
	node := requireNode(t)
	entry, err := Write(filepath.Join(t.TempDir(), "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	gates := filepath.Join(entry, "nightgauge", "gates.js")
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(budgetDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := [][]any{
		{"read", map[string]any{"path": "internal/a.go"}}, // 1: spends the budget
		{"read", map[string]any{"path": "/Users/u/Repositories/nightgauge/nightgauge/.nightgauge/knowledge/features/2087-doctor/PRD.md"}},
		{"read", map[string]any{"path": "/Users/u/Repositories/nightgauge/nightgauge/.nightgauge/knowledge/features/2087-doctor/decisions.md"}},
		{"shell", map[string]any{"command": "cat /Users/u/Repositories/nightgauge/nightgauge/.nightgauge/knowledge/INDEX.md"}},
		{"read", map[string]any{"path": "/kb/custom/features/2087/PRD.md"}},
		{"read", map[string]any{"path": "/kb/other/PRD.md"}},          // refused
		{"read", map[string]any{"path": "/elsewhere/knowledge/x.md"}}, // refused
	}
	raw, _ := json.Marshal(calls)
	cmd := exec.Command(node, driver)
	cmd.Env = append(removeEnv(removeEnv(os.Environ(), EnvExplorationBudget), EnvKnowledgeDir),
		"NG_GATES_PATH="+gates, "NG_CALLS="+string(raw), EnvExplorationBudget+"=1", EnvKnowledgeDir+"=/kb/custom")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("driver output %q: %v", out, err)
	}
	for i, r := range got {
		refused := r != "ok"
		if want := i >= 5; refused != want {
			t.Errorf("call %d %v: got %q, refused want %v", i, calls[i], r, want)
		}
	}
}
