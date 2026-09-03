package gates

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// writeJSON is a test helper for laying out fake skill output JSON.
func writeJSON(t *testing.T, path string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNoOp_AlwaysPasses(t *testing.T) {
	gr := NoOp{GateName: "x"}.Verify(context.Background(), 1, t.TempDir())
	if !gr.Passed {
		t.Fatalf("NoOp must always pass; got reason=%q", gr.Reason)
	}
	if gr.GateName != "x" {
		t.Fatalf("GateName not propagated; got %q", gr.GateName)
	}
}

func TestNoOp_DefaultName(t *testing.T) {
	if name := (NoOp{}).Name(); name != "noop" {
		t.Fatalf("default Name = %q, want %q", name, "noop")
	}
}

func TestGateResult_ToStageGateResult(t *testing.T) {
	gr := GateResult{
		GateName:   "issue-pickup",
		Passed:     false,
		Reason:     "missing context",
		Evidence:   []string{"foo", "bar"},
		DurationMs: 12,
		Timestamp:  "2026-05-07T00:00:00Z",
		Kind:       KindNoOp,
	}
	got := gr.ToStageGateResult()
	if got.GateName != gr.GateName ||
		got.Passed != gr.Passed ||
		got.Reason != gr.Reason ||
		got.DurationMs != gr.DurationMs ||
		got.Timestamp != gr.Timestamp ||
		got.Kind != string(KindNoOp) ||
		len(got.Evidence) != 2 {
		t.Fatalf("ToStageGateResult = %#v, want fields preserved", got)
	}
	if _, ok := any(got).(state.StageGateResult); !ok {
		t.Fatalf("ToStageGateResult must return state.StageGateResult")
	}
	// Mutating the source after copy must not leak into the returned struct.
	gr.Evidence[0] = "mutated"
	if got.Evidence[0] != "foo" {
		t.Fatalf("Evidence not deep-copied; mutation leaked")
	}
}

func TestDefaultRegistry_Has6Gates(t *testing.T) {
	reg := Default()
	required := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	for _, stg := range required {
		if _, ok := reg[stg]; !ok {
			t.Errorf("Default() missing gate for %s", stg)
		}
	}
}

func TestLookupByStageName(t *testing.T) {
	gate, ok := LookupByStageName("issue-pickup")
	if !ok {
		t.Fatalf("LookupByStageName(issue-pickup) returned ok=false")
	}
	if gate.Name() != "issue-pickup" {
		t.Errorf("gate.Name() = %q, want issue-pickup", gate.Name())
	}
	if _, ok := LookupByStageName("not-a-stage"); ok {
		t.Error("expected ok=false for unknown stage")
	}
}

// TestSkillSaidSuccessButGateFailed_AcrossAllGates verifies the canonical
// "skill reported success but gate detected nothing changed" scenario for
// every gate: the skill output context file does not exist, so the gate
// must report passed=false. This is the Issue #3266 contract.
//
// Issue #3267: also asserts that every gate sets Kind=KindNoOp on this
// path so the classifier can emit `skill-no-op` deterministically.
func TestSkillSaidSuccessButGateFailed_AcrossAllGates(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		stage state.PipelineStage
		gate  StageGate
	}{
		{state.StageIssuePickup, IssuePickupGate{}},
		{state.StageFeaturePlanning, FeaturePlanningGate{}},
		{state.StageFeatureDev, FeatureDevGate{}},
		{state.StageFeatureValidate, FeatureValidateGate{}},
		{state.StagePRCreate, PrCreateGate{}},
		{state.StagePRMerge, PrMergeGate{}},
	}
	for _, c := range cases {
		gr := c.gate.Verify(context.Background(), 42, workspace)
		if gr.Passed {
			t.Errorf("%s gate passed when no skill output exists; reason=%q",
				c.stage, gr.Reason)
		}
		if gr.GateName == "" {
			t.Errorf("%s gate did not set GateName", c.stage)
		}
		if gr.Kind != KindNoOp {
			t.Errorf("%s gate Kind = %q, want %q (Issue #3267)",
				c.stage, gr.Kind, KindNoOp)
		}
	}
}

// TestKindOnPass_AcrossAllGates verifies that the timed/timedKind helpers
// produce KindOK on the happy path. Only gates whose pass path doesn't
// require external dependencies (gh, real PR, etc.) are exercised here —
// the others are covered in their per-file tests with mocked dependencies.
func TestKindOnPass_IssuePickup(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "issue-7.json"), map[string]any{
		"issue_number": 7,
		"branch":       "feat/7-x",
	})
	gr := IssuePickupGate{}.Verify(context.Background(), 7, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	if gr.Kind != KindOK {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindOK)
	}
}

// TestKindFailOnMalformedJSON verifies that hard-error branches use KindFail
// rather than KindNoOp — distinct semantics for the classifier (no-op means
// the skill produced nothing; fail means it produced something broken).
func TestKindFailOnMalformedJSON(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "issue-9.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gr := IssuePickupGate{}.Verify(context.Background(), 9, ws)
	if gr.Passed {
		t.Fatalf("expected fail on malformed JSON")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q (malformed JSON is a hard error, not no-op)",
			gr.Kind, KindFail)
	}
}

// TestKindFail_AlwaysCarriesTerminalKind is the #1237 guard: every KindFail
// branch of every gate must classify itself. An empty TerminalKind on a
// KindFail result is not "no opinion" — ResolveTerminalKind falls back to the
// prose ladder, which has no clause for the scheduler's `stage gate failed:
// <reason>` wrapper, so the generic `exit ` rule books the honest gate
// failure as subagent_crash: an infrastructure crash that never happened,
// corrupting failure telemetry and sending auto-triage down a crash-recovery
// path.
//
// One row per KindFail site. Each fixture is arranged so the gate reaches
// that branch and no other (the Reason substring pins which one fired), and
// the assertion is on the kind the site chose, so a new KindFail site cannot
// regress to an empty kind without a row here going red — and a site that
// switches kinds without updating the taxonomy is caught by
// internal/terminalkind's TestCorpus_CoversEveryKind.
func TestKindFail_AlwaysCarriesTerminalKind(t *testing.T) {
	// unreadableContext makes the stage's context path a DIRECTORY so
	// os.ReadFile fails with EISDIR — an error that is not IsNotExist, which
	// is the one shape that reaches the "failed to read" branch rather than
	// the no-op one.
	unreadableContext := func(t *testing.T, ws, contextType string, issue int) {
		t.Helper()
		if err := os.MkdirAll(contextFilePath(ws, contextType, issue), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	devContext := func(t *testing.T, ws string, extra map[string]any) {
		t.Helper()
		payload := map[string]any{
			"files_changed": map[string]any{
				"created":  []string{"foo.go"},
				"modified": []string{},
				"deleted":  []string{},
			},
		}
		for k, v := range extra {
			payload[k] = v
		}
		writeJSON(t, contextFilePath(ws, "dev", 42), payload)
	}

	cases := []struct {
		name       string
		gate       StageGate
		arrange    func(t *testing.T, ws string)
		wantReason string
		wantKind   string
	}{
		{
			name:       "issue-pickup/context unreadable",
			gate:       IssuePickupGate{},
			arrange:    func(t *testing.T, ws string) { unreadableContext(t, ws, "issue", 42) },
			wantReason: "failed to read issue context file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name:       "feature-planning/context unreadable",
			gate:       FeaturePlanningGate{},
			arrange:    func(t *testing.T, ws string) { unreadableContext(t, ws, "planning", 42) },
			wantReason: "failed to read planning context file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name: "feature-planning/plan_file unstatable",
			gate: FeaturePlanningGate{},
			arrange: func(t *testing.T, ws string) {
				// A path whose parent is a regular file stats ENOTDIR, which
				// is not IsNotExist — the only way past the no-op branch.
				parent := filepath.Join(ws, "plan.md")
				if err := os.WriteFile(parent, []byte("# plan"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				writeJSON(t, contextFilePath(ws, "planning", 42), map[string]any{
					"plan_file": filepath.Join(parent, "child.md"),
				})
			},
			wantReason: "failed to stat plan_file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name:       "feature-dev/context unreadable",
			gate:       FeatureDevGate{},
			arrange:    func(t *testing.T, ws string) { unreadableContext(t, ws, "dev", 42) },
			wantReason: "failed to read dev context file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name:       "feature-dev/build_verification missing",
			gate:       FeatureDevGate{},
			arrange:    func(t *testing.T, ws string) { devContext(t, ws, nil) },
			wantReason: "lacks build_verification",
			wantKind:   TerminalKindDevBuildVerificationMissing,
		},
		{
			name: "feature-dev/build_verification failed",
			gate: FeatureDevGate{},
			arrange: func(t *testing.T, ws string) {
				devContext(t, ws, map[string]any{
					"build_verification": map[string]any{"ran": true, "status": "failed"},
				})
			},
			wantReason: "build_verification.status=failed",
			wantKind:   TerminalKindDevBuildVerificationFailed,
		},
		{
			name: "feature-dev/failing tests",
			gate: FeatureDevGate{},
			arrange: func(t *testing.T, ws string) {
				devContext(t, ws, map[string]any{
					"build_verification": map[string]any{"ran": true, "status": "passed"},
					"tests_status":       map[string]any{"failed": 3},
				})
			},
			wantReason: "failing tests",
			wantKind:   TerminalKindDevTestsFailed,
		},
		{
			name: "feature-validate/gate-metrics unreadable",
			gate: FeatureValidateGate{},
			arrange: func(t *testing.T, ws string) {
				// `.nightgauge/health` as a regular file makes the open of
				// health/gate-metrics.jsonl fail ENOTDIR, not ENOENT.
				dir := filepath.Join(ws, ".nightgauge")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "health"), []byte("x"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
			wantReason: "failed to read gate-metrics.jsonl",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name:       "pr-create/context unreadable",
			gate:       PrCreateGate{},
			arrange:    func(t *testing.T, ws string) { unreadableContext(t, ws, "pr", 42) },
			wantReason: "failed to read pr context file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name:       "pr-merge/context unreadable",
			gate:       PrMergeGate{},
			arrange:    func(t *testing.T, ws string) { unreadableContext(t, ws, "pr", 42) },
			wantReason: "failed to read pr context file",
			wantKind:   TerminalKindStageContextUnreadable,
		},
		{
			name: "pr-merge/gh and local git both fail",
			gate: PrMergeGate{},
			arrange: func(t *testing.T, ws string) {
				writeJSON(t, contextFilePath(ws, "pr", 42), map[string]any{"pr_number": 100})
				stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
					return nil, errors.New("gh: connection reset")
				})
				stubExecGitForGate(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
					return nil, errors.New("fatal: not a git repository")
				})
			},
			wantReason: "gh pr view failed after retries",
			wantKind:   TerminalKindPrMergeLookupFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			tc.arrange(t, ws)
			// Relaxed: pr-merge's retry loop sleeps between attempts; one
			// attempt is enough to reach the branch under test.
			gr := tc.gate.Verify(WithRelaxed(context.Background(), true), 42, ws)
			if gr.Passed {
				t.Fatalf("expected fail; reason=%q", gr.Reason)
			}
			if gr.Kind != KindFail {
				t.Fatalf("Kind = %q, want %q — fixture did not reach the KindFail branch (reason=%q)",
					gr.Kind, KindFail, gr.Reason)
			}
			if !strings.Contains(gr.Reason, tc.wantReason) {
				t.Fatalf("reason %q does not contain %q — fixture reached a different branch", gr.Reason, tc.wantReason)
			}
			if gr.TerminalKind == "" {
				t.Errorf("TerminalKind is empty: ResolveTerminalKind would classify `stage gate failed: %s` as subagent_crash (#1237)", gr.Reason)
			}
			if gr.TerminalKind == "subagent_crash" {
				t.Errorf("TerminalKind = subagent_crash: a gate failure is never a process crash (#1237)")
			}
			if gr.TerminalKind != tc.wantKind {
				t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, tc.wantKind)
			}
		})
	}
}
