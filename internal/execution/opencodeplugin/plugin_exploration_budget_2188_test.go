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
const ctx = { directory: "/w/.nightgauge/worktrees/issue-1" };
const out = calls.map(([tool, args]) => {
  try { mod.enforceExplorationBudget(ctx, { tool }, { args }); return "ok"; }
  catch (e) { return e.message; }
});
process.stdout.write(JSON.stringify(out));
`

// TestExplorationBudget2188 proves exploration calls past the budget are
// refused, skill and .nightgauge/ reads and writes are not counted or
// refused, and a budget of 0 or unset is off (#2188).
func TestExplorationBudget2188(t *testing.T) {
	node := requireNode(t)
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	gates := filepath.Join(filepath.Dir(entry), "nightgauge", "gates.js")
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(budgetDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := [][]any{
		{"read", map[string]any{"filePath": "internal/a.go"}},                                // 1
		{"read", map[string]any{"filePath": "skills/_shared/GOTCHAS.md"}},                    // exempt
		{"read", map[string]any{"filePath": "/home/u/.claude/plugins/ng/skills/x/SKILL.md"}}, // exempt
		{"read", map[string]any{"filePath": ".nightgauge/pipeline/ctx.json"}},                // exempt
		{"bash", map[string]any{"command": "ls internal | head -5"}},                         // 2
		{"write", map[string]any{"filePath": ".nightgauge/plans/p.md"}},                      // not exploration
		{"bash", map[string]any{"command": "go test ./..."}},                                 // not exploration
		{"bash", map[string]any{"command": "cat a.go > b.go"}},                               // writes: not exploration
		{"grep", map[string]any{"pattern": "foo"}},                                           // 3
		{"read", map[string]any{"filePath": "internal/b.go"}},                                // refused
		{"bash", map[string]any{"command": "sed -n 1,20p a.go"}},                             // refused
		{"read", map[string]any{"filePath": "skills/x/SKILL.md"}},                            // exempt still
		{"edit", map[string]any{"filePath": "a.go"}},                                         // allowed
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
	refusal := "[nightgauge-gate:exploration-budget] exploration budget of 3 reads spent; write the plan now to .nightgauge/plans/ and planning-42.json"
	for i := range calls {
		want := "ok"
		if i == 9 || i == 10 {
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
