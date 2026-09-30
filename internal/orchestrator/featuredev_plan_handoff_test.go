package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func writePlanningFixture(t *testing.T, ws string, issue int, planningJSON, planRel, plan string) {
	t.Helper()
	ctxPath, err := stagecontext.ContextPath(ws, issue, "planning")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ctxPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctxPath, []byte(planningJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if planRel != "" {
		p := planRel
		if !filepath.IsAbs(p) {
			p = filepath.Join(ws, planRel)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(plan), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRenderPlanHandoffCarriesPlanAndFileLists: the section quotes the plan
// and names each file list, in both entry shapes planning writes (#2181).
func TestRenderPlanHandoffCarriesPlanAndFileLists(t *testing.T) {
	ws := layouttest.Repo(t)
	plan := filepath.Join(layouttest.PlansDir(t, ws), "2087-ledger.md")
	writePlanningFixture(t, ws, 2087,
		`{"plan_file":"`+plan+`",
		  "files_to_modify":[{"path":"internal/github/client.go","reason":"x"},"internal/github/ledgerread.go"],
		  "files_to_create":["internal/github/identity.go"],
		  "files_to_read":[{"file":"internal/github/subprocess.go"}]}`,
		plan,
		"# Plan\n\n- [ ] Step 1: add identity to client.go:120\n")

	got := renderPlanHandoffForPrompt(ws, 2087)
	for _, want := range []string{
		"## Planning hand-off",
		"Do not re-read whole files",
		"### Files to modify", "`internal/github/client.go`", "`internal/github/ledgerread.go`",
		"### Files to create", "`internal/github/identity.go`",
		"`internal/github/subprocess.go`",
		"add identity to client.go:120",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hand-off lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[plan truncated") {
		t.Error("a short plan was marked truncated")
	}
}

// TestRenderPlanHandoffEmptyWithoutAPlan: no planning context, no plan_file,
// a missing plan, or a plan_file outside the plans directory all yield "", so
// the prompt is unchanged.
func TestRenderPlanHandoffEmptyWithoutAPlan(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(ws string){
		"no planning context": func(string) {},
		"no plan_file":        func(ws string) { writePlanningFixture(t, ws, 1, `{"approach":"x"}`, "", "") },
		"missing plan":        func(ws string) { writePlanningFixture(t, ws, 1, `{"plan_file":"plans/none.md"}`, "", "") },
		"plan outside the plans directory": func(ws string) {
			writePlanningFixture(t, ws, 1, `{"plan_file":"`+outside+`"}`, "", "")
		},
		"plan in the worktree": func(ws string) {
			writePlanningFixture(t, ws, 1, `{"plan_file":"PLAN.md"}`, "PLAN.md", "- [ ] In the tree\n")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			ws := layouttest.Repo(t)
			setup(ws)
			if got := renderPlanHandoffForPrompt(ws, 1); got != "" {
				t.Errorf("got a hand-off, want none:\n%s", got)
			}
		})
	}
}

// TestRenderPlanHandoffCapsALongPlan: a plan over the cap is cut and says so.
func TestRenderPlanHandoffCapsALongPlan(t *testing.T) {
	ws := layouttest.Repo(t)
	plan := filepath.Join(layouttest.PlansDir(t, ws), "3-p.md")
	writePlanningFixture(t, ws, 3, `{"plan_file":"`+plan+`"}`, plan, strings.Repeat("x", featureDevPlanHandoffCap+500))
	got := renderPlanHandoffForPrompt(ws, 3)
	if !strings.Contains(got, "[plan truncated") {
		t.Error("a plan over the cap was not marked truncated")
	}
	if len(got) > featureDevPlanHandoffCap+4096 {
		t.Errorf("hand-off is %d bytes, want it bounded near the %d-byte cap", len(got), featureDevPlanHandoffCap)
	}
}
