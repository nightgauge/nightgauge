package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
)

// partialSpliceGolden is internal/orchestrator/testdata/partial-delivery-splice.golden.json.
// The extension's ContextAssembler test reads the same file (#3163 AC 1), so
// one fixture pins both splices to one output.
type partialSpliceGolden struct {
	IssueNumber int             `json:"issue_number"`
	Remainder   json.RawMessage `json:"remainder"`
	Feedback    json.RawMessage `json:"feedback"`
	PlanningIn  json.RawMessage `json:"planning_in"`
	PlanningOut json.RawMessage `json:"planning_out"`
}

func loadPartialSpliceGolden(t *testing.T) partialSpliceGolden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "partial-delivery-splice.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g partialSpliceGolden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

// AC 0/1: the record Go writes is the fixture's remainder, and splicing it
// (with the operator steer) into planning_in yields planning_out.
func TestPartialDeliverySplice_Golden(t *testing.T) {
	g := loadPartialSpliceGolden(t)
	ws := gitWorkspace(t)

	var src struct {
		PR        int       `json:"pr"`
		Criteria  []string  `json:"criteria"`
		Dropped   []string  `json:"dropped"`
		CommentAt time.Time `json:"comment_at"`
	}
	if err := json.Unmarshal(g.Remainder, &src); err != nil {
		t.Fatal(err)
	}
	if err := WritePartialDeliveryContext(ws, g.IssueNumber, PartialDeliveryFile{
		PR: src.PR, Criteria: src.Criteria, Dropped: src.Dropped, CommentAt: &src.CommentAt,
	}); err != nil {
		t.Fatal(err)
	}
	remPath, _ := stagecontext.ContextPath(ws, g.IssueNumber, partialDeliveryContextType)
	written, err := os.ReadFile(remPath)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, written, g.Remainder) {
		t.Fatalf("Go wrote %s, fixture remainder is %s", written, g.Remainder)
	}

	fbPath, _ := stagecontext.ContextPath(ws, g.IssueNumber, "feedback")
	planPath, _ := stagecontext.ContextPath(ws, g.IssueNumber, "planning")
	if err := os.WriteFile(fbPath, g.Feedback, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, g.PlanningIn, 0o644); err != nil {
		t.Fatal(err)
	}
	splicePartialDelivery(ws, g.IssueNumber, planPath)
	spliceOperatorSteer(ws, g.IssueNumber, planPath)
	got, _ := os.ReadFile(planPath)
	if !jsonEqual(t, got, g.PlanningOut) {
		t.Fatalf("spliced planning context:\n%s\nwant:\n%s", got, g.PlanningOut)
	}
}

// No remainder and no steer: the planning context stays byte-identical.
func TestPartialDeliverySplice_AbsentLeavesPlanningAlone(t *testing.T) {
	ws := gitWorkspace(t)
	planPath, _ := stagecontext.ContextPath(ws, 7, "planning")
	_ = os.MkdirAll(filepath.Dir(planPath), 0o755)
	in := []byte(`{"issue_number": 7}`)
	if err := os.WriteFile(planPath, in, 0o644); err != nil {
		t.Fatal(err)
	}
	splicePartialDelivery(ws, 7, planPath)
	spliceOperatorSteer(ws, 7, planPath)
	if got, _ := os.ReadFile(planPath); string(got) != string(in) {
		t.Fatalf("planning context changed: %s", got)
	}
}

func TestRemovePartialDeliveryContext(t *testing.T) {
	ws := gitWorkspace(t)
	if err := RemovePartialDeliveryContext(ws, 9); err != nil {
		t.Fatalf("absent file: %v", err)
	}
	if err := WritePartialDeliveryContext(ws, 9, PartialDeliveryFile{PR: 1, Criteria: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := RemovePartialDeliveryContext(ws, 9); err != nil {
		t.Fatal(err)
	}
	path, _ := stagecontext.ContextPath(ws, 9, partialDeliveryContextType)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

// AC 2/3: the feature-dev block quotes the steer and binds the plan only when
// the plan carries it; otherwise the prompt stays byte-identical.
func TestOperatorRevisionPromptBlock(t *testing.T) {
	if got := renderOperatorRevisionBlock(operatorRevision{FilesToModify: []string{"a.go"}}); got != "" {
		t.Fatalf("no revision, no steer rendered %q", got)
	}
	steerOnly := renderOperatorRevisionBlock(operatorRevision{Steers: []string{"Prefer the small fix."}})
	if !strings.Contains(steerOnly, "> Prefer the small fix.") || strings.Contains(steerOnly, "binding") {
		t.Fatalf("steer-only block = %q", steerOnly)
	}
	bound := renderOperatorRevisionBlock(operatorRevision{
		Revised: true, FilesToModify: []string{"a.go"}, Steers: []string{"line one\nline two"},
	})
	for _, want := range []string{operatorRevisionPromptHeading, "> line one\n> line two", "`a.go`",
		"binding", "PLAN_REVISION_NEEDED", "Never end with zero changes"} {
		if !strings.Contains(bound, want) {
			t.Errorf("binding block lacks %q:\n%s", want, bound)
		}
	}

	g := loadPartialSpliceGolden(t)
	ws := gitWorkspace(t)
	planPath, _ := stagecontext.ContextPath(ws, g.IssueNumber, "planning")
	_ = os.MkdirAll(filepath.Dir(planPath), 0o755)
	if err := os.WriteFile(planPath, g.PlanningOut, 0o644); err != nil {
		t.Fatal(err)
	}
	block := OperatorRevisionPromptBlock(ws, g.IssueNumber)
	if !strings.Contains(block, "Add the integration test the PR skipped.") || !strings.Contains(block, "internal/x/x_integration_test.go") {
		t.Fatalf("block from the golden planning context = %q", block)
	}
	if OperatorRevisionPromptBlock(ws, g.IssueNumber+1) != "" {
		t.Fatal("absent planning context rendered a block")
	}
}

func TestPlanRevisionDispute(t *testing.T) {
	ws := gitWorkspace(t)
	devPath, _ := stagecontext.ContextPath(ws, 5, "dev")
	_ = os.MkdirAll(filepath.Dir(devPath), 0o755)
	write := func(fb string) {
		if err := os.WriteFile(devPath, []byte(`{"feedback":`+fb+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`[{"signal_type":"PLAN_REVISION_NEEDED","evidence":[" "]}]`)
	if got := planRevisionDispute(ws, 5); got != "" {
		t.Fatalf("an evidence-free dispute named %q", got)
	}
	write(`[{"signal_type":"SCOPE_DISCOVERED","evidence":["x"]},{"signal_type":"PLAN_REVISION_NEEDED","evidence":["Task 2","b.go"]}]`)
	if got := planRevisionDispute(ws, 5); got != "Task 2; b.go" {
		t.Fatalf("dispute = %q", got)
	}
}

// DecidePartialNoOp is the decision the extension's stage loop reads through
// `nightgauge issue partial-noop`; it must match the scheduler's (#3163).
func TestDecidePartialNoOp_FromWorkspace(t *testing.T) {
	g := loadPartialSpliceGolden(t)
	ws := gitWorkspace(t)
	n := g.IssueNumber
	planPath, _ := stagecontext.ContextPath(ws, n, "planning")
	_ = os.MkdirAll(filepath.Dir(planPath), 0o755)
	if err := os.WriteFile(planPath, g.PlanningOut, 0o644); err != nil {
		t.Fatal(err)
	}
	scope := &state.PartialRerun{PR: 7, Criteria: []string{"x"}}
	if got := DecidePartialNoOp(ws, n, nil, false); got.Action != "not_scoped" {
		t.Fatalf("unscoped = %+v", got)
	}
	relap := DecidePartialNoOp(ws, n, scope, false)
	if relap.Action != "relap" || !strings.Contains(relap.Steer, "internal/x/x_integration_test.go") {
		t.Fatalf("binding revision = %+v, want relap naming the plan's files", relap)
	}
	if got := DecidePartialNoOp(ws, n, scope, true); got.Action != "book" ||
		ClassifyTerminalKind(got.Detail) != TerminalKindNotPipelineActionable {
		t.Fatalf("second refusal = %+v, want a not_pipeline_actionable booking", got)
	}
	devPath, _ := stagecontext.ContextPath(ws, n, "dev")
	if err := os.WriteFile(devPath, []byte(`{"feedback":[{"signal_type":"PLAN_REVISION_NEEDED","evidence":["Task 2"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DecidePartialNoOp(ws, n, scope, false); got.Action != "dispute" || !strings.Contains(got.Detail, "Task 2") {
		t.Fatalf("named dispute = %+v", got)
	}
}
