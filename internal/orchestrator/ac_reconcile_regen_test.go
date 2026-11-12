package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

func regenCriteria(n int) string {
	var b strings.Builder
	b.WriteString("## Acceptance Criteria\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "- [ ] Behaviour %c of the widget holds\n", 'A'+i)
	}
	return b.String()
}

// writeStaleReport writes the report an earlier run left: 9 criteria.
func writeStaleReport(t *testing.T, ws string) string {
	t.Helper()
	path, err := stagecontext.ContextPath(ws, 42, "ac-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if err := preflight.WriteACReconcile(preflight.ReconcileACs(ws, 42, regenCriteria(9)), path); err != nil {
		t.Fatal(err)
	}
	return path
}

func readReport(t *testing.T, path string) preflight.ACReconcileResult {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec preflight.ACReconcileResult
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// An earlier run's 9-criterion report is replaced by one built from the
// 13-criterion body the issue has now (#3228).
func TestRegenerateACReconcile_ReplacesStaleReport(t *testing.T) {
	ws := gitWorkspace(t)
	path := writeStaleReport(t, ws)
	body := regenCriteria(13)

	s := &Scheduler{trustGate: allWritersGate(), acReconcileBody: func(context.Context, types.BoardItem) (string, error) { return body, nil }}
	if err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws); err != nil {
		t.Fatal(err)
	}
	rec := readReport(t, path)
	if len(rec.AcceptanceCriteria) != 13 {
		t.Errorf("report has %d criteria, want the body's 13", len(rec.AcceptanceCriteria))
	}
	if rec.ACFingerprint != preflight.ACFingerprint(body) {
		t.Errorf("ac_fingerprint = %q, want the body's %q", rec.ACFingerprint, preflight.ACFingerprint(body))
	}
}

// A body that cannot be read fails the regeneration, and the earlier run's
// report is left as it was rather than presented as current.
func TestRegenerateACReconcile_ReadErrorFails(t *testing.T) {
	ws := gitWorkspace(t)
	path := writeStaleReport(t, ws)
	before, _ := os.ReadFile(path)

	s := &Scheduler{trustGate: allWritersGate(), acReconcileBody: func(context.Context, types.BoardItem) (string, error) {
		return "", errors.New("HTTP 502")
	}}
	err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want the read error", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Error("a failed regeneration rewrote the report")
	}
}

// A report that cannot be written fails the regeneration.
func TestRegenerateACReconcile_WriteErrorFails(t *testing.T) {
	ws := gitWorkspace(t)
	path, err := stagecontext.ContextPath(ws, 42, "ac-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the report goes makes the write fail.
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = regenerateACReconcile(context.Background(), ws, 42, func(context.Context) (string, error) {
		return regenCriteria(3), nil
	})
	if err == nil {
		t.Fatal("err = nil, want the write error")
	}
}

// A body with no criteria still writes its (empty) report and posts the
// one-time note the skill's --notify-empty used to post (#2906).
func TestRegenerateACReconcile_NoCriteriaNotifies(t *testing.T) {
	ws := gitWorkspace(t)
	notified := 0
	s := &Scheduler{
		trustGate:       allWritersGate(),
		acReconcileBody: func(context.Context, types.BoardItem) (string, error) { return "No criteria here.", nil },
		acEmptyNotify:   func(context.Context, types.BoardItem) error { notified++; return nil },
	}
	if err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws); err != nil {
		t.Fatal(err)
	}
	path, _ := stagecontext.ContextPath(ws, 42, "ac-reconcile")
	if rec := readReport(t, path); rec.AggregateStatus != "no-acs-detected" {
		t.Errorf("aggregate = %q, want no-acs-detected", rec.AggregateStatus)
	}
	if notified != 1 {
		t.Errorf("notified %d times, want 1", notified)
	}
}

func planningPipelineScheduler(t *testing.T) (*Scheduler, *refusalCapturingStageRunner) {
	t.Helper()
	root := gitWorkspace(t)
	for _, dir := range []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	runner := newRefusalCapturingStageRunner()
	return newRefusalScheduler(root, runner), runner
}

// The scheduler rebuilds the report before feature-planning dispatches: the
// planning stage finds the current body's 13 criteria beside its output.
func TestScheduler_RegeneratesACReconcileBeforePlanning(t *testing.T) {
	s, runner := planningPipelineScheduler(t)
	body := regenCriteria(13)
	s.acReconcileBody = func(context.Context, types.BoardItem) (string, error) { return body, nil }

	s.runPipeline(context.Background(), types.BoardItem{Number: 3228, Repo: "nightgauge/nightgauge", ID: "item-3228"})

	if got := runner.count(state.StageFeaturePlanning); got != 1 {
		t.Fatalf("feature-planning ran %d times, want 1", got)
	}
	out := runner.outputFile(state.StageFeaturePlanning)
	rec := readReport(t, filepath.Join(filepath.Dir(out), "ac-reconcile-3228.json"))
	if len(rec.AcceptanceCriteria) != 13 || rec.ACFingerprint != preflight.ACFingerprint(body) {
		t.Errorf("report = %d criteria, fingerprint %q; want the body's 13 and %q",
			len(rec.AcceptanceCriteria), rec.ACFingerprint, preflight.ACFingerprint(body))
	}
}

// A failed regeneration fails feature-planning before it dispatches, with the
// cause recorded under the stage.
func TestScheduler_FailedACReconcileRegenerationFailsTheStage(t *testing.T) {
	s, runner := planningPipelineScheduler(t)
	s.acReconcileBody = func(context.Context, types.BoardItem) (string, error) {
		return "", errors.New("HTTP 502")
	}

	s.runPipeline(context.Background(), types.BoardItem{Number: 3228, Repo: "nightgauge/nightgauge", ID: "item-3228"})

	if got := runner.count(state.StageFeaturePlanning); got != 0 {
		t.Fatalf("feature-planning dispatched %d time(s); a failed regeneration must stop it", got)
	}
	snap := runner.runtime.Snapshot()
	if snap.Stage != state.StageFeaturePlanning {
		t.Fatalf("snap.Stage = %q, want feature-planning", snap.Stage)
	}
	if reason := snap.StageErrors[string(snap.Stage)]; !strings.Contains(reason, "ac-reconcile regeneration failed") ||
		!strings.Contains(reason, "HTTP 502") {
		t.Errorf("stage error = %q, want the regeneration failure", reason)
	}
}

// A body whose author has write access is vouched for after the read and is
// reconciled (#3231).
func TestRegenerateACReconcile_VouchedBodyReconciles(t *testing.T) {
	ws := gitWorkspace(t)
	path := writeStaleReport(t, ws)
	body := regenCriteria(13)
	gate, f := levelsGate(map[string]trust.Level{"maintainer": trust.LevelWrite})
	f.signals["o/r#42"] = trust.Signals{Author: "maintainer"}

	s := &Scheduler{trustGate: gate, acReconcileBody: func(context.Context, types.BoardItem) (string, error) { return body, nil }}
	if err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws); err != nil {
		t.Fatal(err)
	}
	if rec := readReport(t, path); rec.ACFingerprint != preflight.ACFingerprint(body) {
		t.Errorf("ac_fingerprint = %q, want the vouched body's", rec.ACFingerprint)
	}
}

// An outsider's body is refused after the read: the stage errors with the
// gate's reason and the earlier report is neither replaced nor used (#3231).
func TestRegenerateACReconcile_UnvouchedBodyRefused(t *testing.T) {
	ws := gitWorkspace(t)
	path := writeStaleReport(t, ws)
	before, _ := os.ReadFile(path)
	gate, f := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead})
	f.signals["o/r#42"] = trust.Signals{Author: "stranger"}

	s := &Scheduler{trustGate: gate, acReconcileBody: func(context.Context, types.BoardItem) (string, error) {
		return regenCriteria(13), nil
	}}
	err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws)
	if err == nil || !strings.Contains(err.Error(), "not vouched for") {
		t.Fatalf("err = %v, want a not-vouched refusal", err)
	}
	if strings.HasSuffix(strings.TrimSpace(err.Error()), "read time:") {
		t.Errorf("err = %v, want it to carry the gate's reason", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Error("the earlier report was rewritten from an unvouched body")
	}
}

// A forge that cannot answer the trust question fails closed (#3231).
func TestRegenerateACReconcile_UndecidableFailsClosed(t *testing.T) {
	ws := gitWorkspace(t)
	path := writeStaleReport(t, ws)
	before, _ := os.ReadFile(path)
	for name, gate := range map[string]*trust.Gate{
		"forge cannot answer": trust.NewGate(&fakeTrustForge{}, nil),
		"no gate":             nil,
	} {
		s := &Scheduler{trustGate: gate, acReconcileBody: func(context.Context, types.BoardItem) (string, error) {
			return regenCriteria(13), nil
		}}
		if err := s.regenerateACReconcileFor(context.Background(), types.BoardItem{Number: 42, Repo: "o/r"}, ws); err == nil {
			t.Errorf("%s: regeneration succeeded, want fail-closed", name)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Errorf("%s: report was rewritten", name)
		}
	}
}
