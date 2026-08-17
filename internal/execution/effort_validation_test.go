package execution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
)

// fakeEffortAdapter is the ValidateEffort analogue of unstartableAdapter
// (worktree_stamp_test.go): a SkillRunner that implements the optional
// ValidateEffort(model, effort string) error hook and records everything
// RunStage does with it — the exact (model, effort) pair the hook received,
// how many times it was called, and whether BuildCommand (the step
// immediately after the hook, and therefore the earliest observable proxy
// for "a spawn was attempted") ever ran. bin names a binary that does not
// exist, so IF control reaches cmd.Start() the failure is unambiguous and
// distinguishable from an effort-validation rejection.
type fakeEffortAdapter struct {
	bin       string
	effortErr error

	validateCalls int
	gotModel      string
	gotEffort     string
	buildCalled   bool
}

func (a *fakeEffortAdapter) Name() string { return "test-fake-effort" }

func (a *fakeEffortAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	a.buildCalled = true
	return a.bin, nil, nil
}

func (a *fakeEffortAdapter) UsesStdin() bool { return false }

func (a *fakeEffortAdapter) Agentic() bool { return true }

// ValidateEffort is the hook under test. Manager.RunStage reaches it through
// an ANONYMOUS interface assertion (`adapter.(interface{ ValidateEffort(...)
// error })`), not a named interface — so this exact method name and
// signature is load-bearing. Rename it and RunStage's `ok` silently goes
// false: no compile error, no panic, just a pre-spawn gate that stops firing.
func (a *fakeEffortAdapter) ValidateEffort(model, effort string) error {
	a.validateCalls++
	a.gotModel = model
	a.gotEffort = effort
	return a.effortErr
}

// fakeAdapterNoEffort is fakeEffortAdapter minus the ValidateEffort method —
// a distinct type rather than a flag, because the thing under test is a
// static interface assertion: Go has no way to make one type "not have" a
// method at runtime, so proving the hook is optional requires an adapter
// that structurally lacks it.
type fakeAdapterNoEffort struct {
	bin string

	buildCalled bool
}

func (a *fakeAdapterNoEffort) Name() string { return "test-fake-no-effort" }

func (a *fakeAdapterNoEffort) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	a.buildCalled = true
	return a.bin, nil, nil
}

func (a *fakeAdapterNoEffort) UsesStdin() bool { return false }

func (a *fakeAdapterNoEffort) Agentic() bool { return true }

// TestRunStage_ValidateEffort_AbortsBeforeSpawn is the ValidateEffort twin of
// the "ValidateModel rejects the model before the command is built" case in
// TestRunStage_StampsWorktreeOnRuntime_WhenStageFailsBeforeSpawn
// (worktree_stamp_test.go) — the existing RunStage-level test for the
// ValidateModel hook immediately above ValidateEffort in manager.go.
//
// No prior test drove RunStage's anonymous ValidateEffort assertion at all
// (#634): a renamed method, an added/removed parameter, or a pointer/value
// receiver flip on any adapter's ValidateEffort makes the type assertion's
// `ok` silently false, and the pre-spawn effort gate stops firing with no
// compile error and no test failure — the documented silent-guard class.
//
// This uses a FAKE adapter (not a real one) specifically so the test can
// assert what a real-adapter test cannot: the exact (model, effort) pair the
// hook received, and — the load-bearing assertion — that BuildCommand was
// never invoked once ValidateEffort rejected. A test that only checked the
// returned error string would go green even if the abort fired too late.
func TestRunStage_ValidateEffort_AbortsBeforeSpawn(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	const issue = 634

	repoRoot := realGitRepo(t)
	fake := &fakeEffortAdapter{
		bin:       filepath.Join(t.TempDir(), "no-such-agent-cli"),
		effortErr: errors.New("effort xhigh is not valid for model grok-4"),
	}

	m := NewManager(repoRoot, fake)
	rt := state.NewRuntimeState(repo, issue, "", "01890a5d-ac96-774b-bcce-b302099a8058")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := m.RunStage(ctx, StageOptions{
		Repo:        repo,
		IssueNumber: issue,
		Stage:       "feature-dev",
		Model:       "grok-4",
		Effort:      "xhigh",
		Timeout:     30 * time.Second,
		Runtime:     rt,
	})

	if err == nil {
		t.Fatalf("expected RunStage to fail when ValidateEffort rejects, got result %+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result on effort-validation error, got %+v", result)
	}

	wantMsg := `effort validation failed for adapter "test-fake-effort"`
	if !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), wantMsg)
	}
	if !strings.Contains(err.Error(), fake.effortErr.Error()) {
		t.Errorf("error = %q, want it to wrap the underlying %q", err.Error(), fake.effortErr.Error())
	}

	if fake.validateCalls != 1 {
		t.Fatalf("ValidateEffort called %d times, want exactly 1", fake.validateCalls)
	}
	if fake.gotModel != "grok-4" {
		t.Errorf("ValidateEffort received model %q, want %q", fake.gotModel, "grok-4")
	}
	if fake.gotEffort != "xhigh" {
		t.Errorf("ValidateEffort received effort %q, want %q", fake.gotEffort, "xhigh")
	}

	// The load-bearing assertion: BuildCommand — and therefore cmd.Start() —
	// must never be reached once ValidateEffort has rejected. Renaming
	// fakeEffortAdapter.ValidateEffort (breaking the anonymous interface
	// assertion in manager.go) makes this flag come back true, because
	// RunStage would then fall through to spawn instead of aborting.
	if fake.buildCalled {
		t.Error("BuildCommand was called — ValidateEffort's error must abort BEFORE spawn, not after")
	}
	if pid := rt.StageChildPID(); pid != 0 {
		t.Errorf("runtime.PID = %d, want 0 — a rejected effort must never register a child process", pid)
	}
}

// TestRunStage_ValidateEffort_OptionalHook_SkippedWhenAdapterLacksMethod is
// the companion required by #634: an adapter that does NOT implement
// ValidateEffort must still run. manager.go's ValidateEffort assertion is
// deliberately anonymous and optional — every adapter without the method is
// unaffected — and this pins that half of the contract the same way the
// abort case above pins the other half.
func TestRunStage_ValidateEffort_OptionalHook_SkippedWhenAdapterLacksMethod(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	const issue = 634

	repoRoot := realGitRepo(t)
	fake := &fakeAdapterNoEffort{bin: filepath.Join(t.TempDir(), "no-such-agent-cli")}

	m := NewManager(repoRoot, fake)
	rt := state.NewRuntimeState(repo, issue, "", "01890a5d-ac96-774b-bcce-b302099a8059")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := m.RunStage(ctx, StageOptions{
		Repo:        repo,
		IssueNumber: issue,
		Stage:       "feature-dev",
		Model:       "grok-4",
		Effort:      "xhigh",
		Timeout:     30 * time.Second,
		Runtime:     rt,
	})

	if err == nil {
		t.Fatalf("expected RunStage to fail at cmd.Start (nothing can start %q), got result %+v", fake.bin, result)
	}
	if strings.Contains(err.Error(), "effort validation failed") {
		t.Fatalf("error = %q — an adapter with no ValidateEffort method must never be blocked by the effort hook", err.Error())
	}
	if !strings.Contains(err.Error(), "start ") {
		t.Fatalf("error = %q, want it to fail at cmd.Start — proof RunStage proceeded past the optional, absent hook to spawn", err.Error())
	}

	// The hook being "genuinely optional" means RunStage proceeds past it to
	// BuildCommand even though fakeAdapterNoEffort has no ValidateEffort
	// method at all.
	if !fake.buildCalled {
		t.Error("BuildCommand was never called — RunStage should have proceeded past the optional effort hook to spawn")
	}
}
