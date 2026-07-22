package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// stubAction is a configurable RecoveryAction used by registry-level tests.
type stubAction struct {
	name      string
	matches   func(StageFailure) bool
	executeFn func(StageFailure) RecoveryResult
}

func (s *stubAction) Name() string        { return s.name }
func (s *stubAction) Description() string { return "stub for tests: " + s.name }
func (s *stubAction) Matches(f StageFailure) bool {
	if s.matches == nil {
		return true
	}
	return s.matches(f)
}
func (s *stubAction) Execute(_ context.Context, f StageFailure) RecoveryResult {
	if s.executeFn == nil {
		return RecoveryResult{Recovered: true, Action: s.name}
	}
	return s.executeFn(f)
}

// TestRegistry_OrderingFirstMatchWins registers two actions whose Matches
// predicates overlap and asserts the first registered wins.
func TestRegistry_OrderingFirstMatchWins(t *testing.T) {
	first := &stubAction{
		name: "first",
		executeFn: func(_ StageFailure) RecoveryResult {
			return RecoveryResult{Recovered: true, Action: "first"}
		},
	}
	second := &stubAction{
		name: "second",
		executeFn: func(_ StageFailure) RecoveryResult {
			return RecoveryResult{Recovered: true, Action: "second"}
		},
	}
	r := New(0, first, second) // unlimited cap

	res, matched := r.TryRecover(context.Background(), StageFailure{
		Stage:       state.StagePRMerge,
		IssueNumber: 42,
	}, 0)
	if !matched {
		t.Fatalf("expected match, got false")
	}
	if res.Action != "first" {
		t.Errorf("expected first to win, got %q", res.Action)
	}
}

// TestRegistry_HonoursCap verifies that the per-run cap stops further
// recovery attempts once the threshold is reached.
func TestRegistry_HonoursCap(t *testing.T) {
	a := &stubAction{name: "always-match"}
	r := New(2, a)

	if _, matched := r.TryRecover(context.Background(), StageFailure{}, 0); !matched {
		t.Fatal("attempt 1: expected match")
	}
	if _, matched := r.TryRecover(context.Background(), StageFailure{}, 1); !matched {
		t.Fatal("attempt 2: expected match")
	}
	if _, matched := r.TryRecover(context.Background(), StageFailure{}, 2); matched {
		t.Fatal("attempt 3: cap should have prevented match")
	}
}

// TestRegistry_NoActions returns false unconditionally — the empty registry
// is a valid no-op.
func TestRegistry_NoActions(t *testing.T) {
	r := New(3)
	if _, matched := r.TryRecover(context.Background(), StageFailure{}, 0); matched {
		t.Fatal("empty registry must not match")
	}
}

// TestRegistry_RecordsFailedRecovery exercises the matched-but-declined path:
// the action returned Recovered=false. The registry still reports matched=true
// so the caller records the attempt for telemetry.
func TestRegistry_RecordsFailedRecovery(t *testing.T) {
	a := &stubAction{
		name: "declined",
		executeFn: func(_ StageFailure) RecoveryResult {
			return RecoveryResult{Recovered: false, Action: "declined", Reason: "preconditions changed", FollowUp: FollowUpHumanTriageRequired}
		},
	}
	r := New(3, a)
	res, matched := r.TryRecover(context.Background(), StageFailure{}, 0)
	if !matched {
		t.Fatal("expected match even though Recovered=false")
	}
	if res.Recovered {
		t.Fatal("expected Recovered=false")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
}

// TestRegistry_NoMatch_FallsThrough verifies that when no action's Matches
// predicate fires, TryRecover returns matched=false and the action is never
// executed.
func TestRegistry_NoMatch_FallsThrough(t *testing.T) {
	executed := false
	a := &stubAction{
		name:    "should-not-fire",
		matches: func(_ StageFailure) bool { return false },
		executeFn: func(_ StageFailure) RecoveryResult {
			executed = true
			return RecoveryResult{Recovered: true}
		},
	}
	r := New(3, a)
	if _, matched := r.TryRecover(context.Background(), StageFailure{}, 0); matched {
		t.Fatal("expected no match")
	}
	if executed {
		t.Fatal("Execute must not run when Matches returns false")
	}
}

// TestRegistry_ActionDefaultsName ensures Execute results that omit Action
// pick up the registered action name.
func TestRegistry_ActionDefaultsName(t *testing.T) {
	a := &stubAction{
		name: "named-action",
		executeFn: func(_ StageFailure) RecoveryResult {
			return RecoveryResult{Recovered: true} // intentionally no Action
		},
	}
	r := New(3, a)
	res, _ := r.TryRecover(context.Background(), StageFailure{}, 0)
	if res.Action != "named-action" {
		t.Errorf("Action = %q, want defaulted to action.Name() %q", res.Action, "named-action")
	}
}

// TestGetMaxAttemptsPerRun_DefaultWhenAbsent returns the constant default
// when no config / env override is present.
func TestGetMaxAttemptsPerRun_DefaultWhenAbsent(t *testing.T) {
	t.Setenv(EnvMaxAttemptsPerRun, "")
	if n := GetMaxAttemptsPerRun(t.TempDir()); n != DefaultMaxAttemptsPerRun {
		t.Errorf("default = %d, want %d", n, DefaultMaxAttemptsPerRun)
	}
}

// TestGetMaxAttemptsPerRun_EnvOverride wins over YAML config.
func TestGetMaxAttemptsPerRun_EnvOverride(t *testing.T) {
	t.Setenv(EnvMaxAttemptsPerRun, "7")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    max_attempts_per_run: 5\n")
	if n := GetMaxAttemptsPerRun(root); n != 7 {
		t.Errorf("env override n=%d, want 7", n)
	}
}

// TestGetMaxAttemptsPerRun_YAMLNested reads the nested form.
func TestGetMaxAttemptsPerRun_YAMLNested(t *testing.T) {
	t.Setenv(EnvMaxAttemptsPerRun, "")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    max_attempts_per_run: 5\n")
	if n := GetMaxAttemptsPerRun(root); n != 5 {
		t.Errorf("yaml n=%d, want 5", n)
	}
}

// TestGetMaxAttemptsPerRun_RejectsNonPositive falls back to default for
// nonsense values (zero or negative integers in YAML).
func TestGetMaxAttemptsPerRun_RejectsNonPositive(t *testing.T) {
	t.Setenv(EnvMaxAttemptsPerRun, "")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    max_attempts_per_run: 0\n")
	if n := GetMaxAttemptsPerRun(root); n != DefaultMaxAttemptsPerRun {
		t.Errorf("zero in yaml: n=%d, want default %d", n, DefaultMaxAttemptsPerRun)
	}
}

// TestGetConflictMaxDevRedispatch_DefaultWhenAbsent returns the constant
// default when no config / env override is present (#4072).
func TestGetConflictMaxDevRedispatch_DefaultWhenAbsent(t *testing.T) {
	t.Setenv(EnvConflictMaxDevRedispatch, "")
	if n := GetConflictMaxDevRedispatch(t.TempDir()); n != DefaultConflictMaxDevRedispatch {
		t.Errorf("default = %d, want %d", n, DefaultConflictMaxDevRedispatch)
	}
}

// TestGetConflictMaxDevRedispatch_EnvOverride wins over YAML config.
func TestGetConflictMaxDevRedispatch_EnvOverride(t *testing.T) {
	t.Setenv(EnvConflictMaxDevRedispatch, "5")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    conflict_recovery:\n      max_dev_redispatch: 3\n")
	if n := GetConflictMaxDevRedispatch(root); n != 5 {
		t.Errorf("env override n=%d, want 5", n)
	}
}

// TestGetConflictMaxDevRedispatch_YAMLNested reads the three-level nested form.
func TestGetConflictMaxDevRedispatch_YAMLNested(t *testing.T) {
	t.Setenv(EnvConflictMaxDevRedispatch, "")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    conflict_recovery:\n      enabled: true\n      max_dev_redispatch: 4\n")
	if n := GetConflictMaxDevRedispatch(root); n != 4 {
		t.Errorf("yaml n=%d, want 4", n)
	}
}

// TestGetConflictMaxDevRedispatch_RejectsNonPositive falls back to default.
func TestGetConflictMaxDevRedispatch_RejectsNonPositive(t *testing.T) {
	t.Setenv(EnvConflictMaxDevRedispatch, "")
	root := t.TempDir()
	writeConfigYAML(t, root, "pipeline:\n  recovery:\n    conflict_recovery:\n      max_dev_redispatch: 0\n")
	if n := GetConflictMaxDevRedispatch(root); n != DefaultConflictMaxDevRedispatch {
		t.Errorf("zero in yaml: n=%d, want default %d", n, DefaultConflictMaxDevRedispatch)
	}
}

func writeConfigYAML(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
}

// TestToStateRecoveryAttempt populates the state-side struct including a
// timestamp.
func TestToStateRecoveryAttempt(t *testing.T) {
	r := RecoveryResult{
		Recovered: true,
		Action:    "ci-rerun",
		Reason:    "rerun ok",
		Evidence:  []string{"run=123"},
		FollowUp:  FollowUpStageCanResume,
		CostUSD:   0,
	}
	s := ToStateRecoveryAttempt(r)
	if s.Action != "ci-rerun" || !s.Recovered || s.At == "" {
		t.Errorf("unexpected state attempt: %+v", s)
	}
	if len(s.Evidence) != 1 || s.Evidence[0] != "run=123" {
		t.Errorf("evidence not copied: %v", s.Evidence)
	}
}
