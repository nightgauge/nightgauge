package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// expireOnRunner holds the first command starting with prefix until the gate's
// deadline passes, the way a real suite overrunning its limit does.
type expireOnRunner struct {
	*mockCmdRunner
	prefix string
}

func (r *expireOnRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	if strings.HasPrefix(key, r.prefix) {
		<-ctx.Done()
		return []byte("signal: killed"), errors.New("signal: killed")
	}
	return r.mockCmdRunner.Run(ctx, dir, name, args...)
}

func goWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	layouttest.MkPipelineDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func evaluateExpiring(t *testing.T, prefix string) (PrePushResult, string) {
	t.Helper()
	dir := goWorkDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner := &expireOnRunner{mockCmdRunner: setupCleanBranchMock(), prefix: prefix}
	return EvaluatePrePush(ctx, runner, PrePushInput{
		IssueNumber: 42, WorkDir: dir, TargetBranch: "main", TimeoutSeconds: 2400,
	}), dir
}

func TestEvaluatePrePush_DeadlineInTestPhaseIsATimeout(t *testing.T) {
	result, dir := evaluateExpiring(t, "go test")

	if result.ValidationPhases["test"] != "timeout" || result.FailureKind != FailureKindTimeout {
		t.Fatalf("phases=%v kind=%q", result.ValidationPhases, result.FailureKind)
	}
	if strings.Contains(result.Reason, "Tests failed") {
		t.Errorf("a deadline was reported as a test failure: %s", result.Reason)
	}
	for _, want := range []string{"test phase", "2400s", "hooks.pre_push.timeout_seconds", "--timeout"} {
		if !strings.Contains(result.Reason, want) {
			t.Errorf("reason lacks %q: %s", want, result.Reason)
		}
	}
	written := ReadPrePushContext(dir, 42)
	if written == nil {
		t.Fatal("context file missing")
	}
	if written.OverallStatus != "timeout" || !written.Blocking || written.FailureKind != FailureKindTimeout {
		t.Errorf("context = %+v", written)
	}
}

func TestEvaluatePrePush_DeadlineDuringMergeIsNotAConflict(t *testing.T) {
	result, _ := evaluateExpiring(t, "git merge --no-commit")
	if result.ValidationPhases["merged_state"] != "timeout" || strings.Contains(result.Reason, "Merge conflict") {
		t.Errorf("phases=%v reason=%s", result.ValidationPhases, result.Reason)
	}
}

func TestEvaluatePrePush_DeadlineInSecurityPhaseBlocks(t *testing.T) {
	// The scan swallows a failing diff, so only the post-phase check can catch this.
	result, _ := evaluateExpiring(t, "gitleaks")
	if result.Decision != "block" || result.ValidationPhases["security"] != "timeout" {
		t.Errorf("an expired gate passed unscanned: decision=%s phases=%v", result.Decision, result.ValidationPhases)
	}
}

func TestEvaluatePrePush_GoPerPackageTimeoutIsATimeout(t *testing.T) {
	dir := goWorkDir(t)
	m := setupCleanBranchMock()
	m.set("go test -timeout 2400s ./...", "panic: test timed out after 40m0s", errors.New("exit status 1"))
	result := EvaluatePrePush(context.Background(), m, PrePushInput{
		IssueNumber: 42, WorkDir: dir, TargetBranch: "main", TimeoutSeconds: 2400,
	})
	if result.ValidationPhases["test"] != "timeout" || !strings.Contains(result.Reason, "per-package") {
		t.Errorf("phases=%v reason=%s", result.ValidationPhases, result.Reason)
	}
}

func TestEvaluatePrePush_RealTestFailureStaysFailed(t *testing.T) {
	dir := goWorkDir(t)
	m := setupCleanBranchMock()
	m.set("go test -timeout 2400s ./...", "--- FAIL: TestX", errors.New("exit status 1"))
	result := EvaluatePrePush(context.Background(), m, PrePushInput{
		IssueNumber: 42, WorkDir: dir, TargetBranch: "main", TimeoutSeconds: 2400,
	})
	if result.ValidationPhases["test"] != "failed" || result.FailureKind != "" ||
		!strings.Contains(result.Reason, "Tests failed") {
		t.Errorf("phases=%v kind=%q reason=%s", result.ValidationPhases, result.FailureKind, result.Reason)
	}
}

func TestGoTestTimeoutArg_FiresBeforeTheGateDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2400*time.Second)
	defer cancel()
	got, err := time.ParseDuration(goTestTimeoutArg(ctx, 2400))
	if err != nil {
		t.Fatal(err)
	}
	if want := 2340 * time.Second; got > want || got < want-5*time.Second {
		t.Errorf("timer = %s, want about %s", got, want)
	}

	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShort()
	if got, want := goTestTimeoutArg(short, 2400), "60s"; got != want {
		t.Errorf("nearly spent budget: timer = %s, want floor %s", got, want)
	}
	if got, want := goTestTimeoutArg(ctx, 0), "0"; got != want {
		t.Errorf("no limit: timer = %s, want %s", got, want)
	}
}

func TestEvaluatePrePush_GoTestTimeoutArgument(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{{2400, "2400s"}, {0, "0"}} {
		dir := goWorkDir(t)
		m := setupCleanBranchMock()
		EvaluatePrePush(context.Background(), m, PrePushInput{
			IssueNumber: 42, WorkDir: dir, TargetBranch: "main", TimeoutSeconds: tc.seconds,
		})
		found := false
		for _, c := range m.calls {
			if c.Name == "go" && strings.Join(c.Args, " ") == "test -timeout "+tc.want+" ./..." {
				found = true
			}
		}
		if !found {
			t.Errorf("seconds=%d: no go test with -timeout %s in %v", tc.seconds, tc.want, m.calls)
		}
	}
}

func TestExecCmdRunner_KilledCommandDoesNotWaitOnItsChildren(t *testing.T) {
	old := runnerWaitDelay
	runnerWaitDelay = 100 * time.Millisecond
	defer func() { runnerWaitDelay = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&ExecCmdRunner{}).Run(ctx, t.TempDir(), "sh", "-c", "sleep 5 & wait")
	if err == nil {
		t.Error("a killed command returned success")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run blocked %s on a child holding the pipe", elapsed)
	}
}

func TestExecCmdRunner_LingeringChildIsNotAFailure(t *testing.T) {
	old := runnerWaitDelay
	runnerWaitDelay = 100 * time.Millisecond
	defer func() { runnerWaitDelay = old }()

	if _, err := (&ExecCmdRunner{}).Run(context.Background(), t.TempDir(), "sh", "-c", "sleep 2 &"); err != nil {
		t.Errorf("a command that exited 0 reported %v", err)
	}
}
