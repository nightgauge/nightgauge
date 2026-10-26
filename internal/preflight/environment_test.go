//go:build !windows

package preflight

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

func envCheck(name string, run ...string) config.EnvironmentCheck {
	return config.EnvironmentCheck{Name: name, Run: run, Remedy: "fix " + name}
}

func TestEnvironment_NoneDeclared(t *testing.T) {
	res := RunEnvironmentChecks(context.Background(), nil, t.TempDir())
	b, _ := json.Marshal(res)
	if string(b) != `{"ok":true,"checks":[]}` {
		t.Fatalf("got %s", b)
	}
}

func TestEnvironment_TrueAndFalse(t *testing.T) {
	res := RunEnvironmentChecks(context.Background(),
		[]config.EnvironmentCheck{envCheck("good", "true"), envCheck("bad", "false")}, t.TempDir())
	if res.OK {
		t.Fatal("OK = true with a failing check")
	}
	if res.Checks[0].Status != "ok" {
		t.Errorf("true: %+v", res.Checks[0])
	}
	bad := res.Checks[1]
	if bad.Status != "failed" || bad.Reason != EnvReasonExitCode || bad.ExitCode != 1 || bad.Remedy != "fix bad" {
		t.Errorf("false: %+v", bad)
	}
}

func TestEnvironment_StdoutNumberNamesObservedAndBound(t *testing.T) {
	min := 2000.0
	c := envCheck("storage", "echo", "/data 1500 75%")
	c.Expect.StdoutNumber = &config.StdoutNumberExpect{Capture: `(\d+)\s+\d+%`, Min: &min}
	r := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{c}, t.TempDir()).Checks[0]
	if r.Reason != EnvReasonNumberOutOfRange || r.Message != "observed 1500, want >= 2000" {
		t.Fatalf("got %+v", r)
	}
	if r.Observed == nil || *r.Observed != 1500 {
		t.Fatalf("observed = %v", r.Observed)
	}
	min = 1000
	if r := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{c}, t.TempDir()).Checks[0]; r.Status != "ok" {
		t.Fatalf("in range should pass: %+v", r)
	}
}

func TestEnvironment_StdoutMatchesAndExitCode(t *testing.T) {
	two := 2
	m := envCheck("match", "echo", "status: healthy")
	m.Expect.StdoutMatches = `: healthy`
	miss := envCheck("miss", "echo", "status: down")
	miss.Expect.StdoutMatches = `healthy`
	code := envCheck("code", "sh", "-c", "exit 2")
	code.Expect.ExitCode = &two
	res := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{m, miss, code}, t.TempDir())
	if res.Checks[0].Status != "ok" || res.Checks[2].Status != "ok" {
		t.Errorf("unexpected failure: %+v", res.Checks)
	}
	if res.Checks[1].Reason != EnvReasonStdoutMismatch {
		t.Errorf("miss: %+v", res.Checks[1])
	}
}

func TestEnvironment_NoShellInterpretation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	c := envCheck("argv", "echo", "x; touch "+marker)
	r := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{c}, t.TempDir()).Checks[0]
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("argument was interpreted by a shell")
	}
	if !strings.Contains(r.Stdout, "; touch") {
		t.Fatalf("stdout = %q", r.Stdout)
	}
}

func TestEnvironment_StartFailed(t *testing.T) {
	r := RunEnvironmentChecks(context.Background(),
		[]config.EnvironmentCheck{envCheck("missing", "definitely-not-a-binary-101")}, t.TempDir()).Checks[0]
	if r.Reason != EnvReasonStartFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestEnvironment_StdoutCapped(t *testing.T) {
	c := envCheck("chatty", "sh", "-c", "head -c 100000 /dev/zero | tr '\\0' a")
	r := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{c}, t.TempDir()).Checks[0]
	if len(r.Stdout) != EnvironmentOutputCap || !r.StdoutTruncated {
		t.Fatalf("len=%d truncated=%v", len(r.Stdout), r.StdoutTruncated)
	}
}

// A check that outlives its timeout fails with reason "timeout", and the
// child it spawned is killed with it rather than left running.
func TestEnvironment_TimeoutReapsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := envCheck("slow", "sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	c.Timeout = config.YAMLDuration(300 * time.Millisecond)
	start := time.Now()
	r := RunEnvironmentChecks(context.Background(), []config.EnvironmentCheck{c}, t.TempDir()).Checks[0]
	if r.Reason != EnvReasonTimeout {
		t.Fatalf("got %+v", r)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s — timeout not enforced", time.Since(start))
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("child pid not recorded: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child sleep %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
