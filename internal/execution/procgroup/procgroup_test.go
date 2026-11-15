package procgroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunKillsTheGroupOnEveryExitPath: whatever way the leader ends, a zero
// exit, a non-zero exit, a timeout or a cancel, the child it backgrounded in
// its group is gone when Run returns, and a child holding the leader's stdout
// pipe does not hold Run until WaitDelay (#3303).
func TestRunKillsTheGroupOnEveryExitPath(t *testing.T) {
	cases := []struct {
		name string
		// tail runs after the backgrounded child's pid is recorded.
		tail string
		// holdStdout leaves the child on the leader's stdout pipe.
		holdStdout bool
		ctx        func() (context.Context, context.CancelFunc)
		wantExit   int // -1: killed by ctx, so no exit code is asserted
	}{
		{name: "exit 0", tail: "exit 0", wantExit: 0},
		{name: "exit 3", tail: "exit 3", wantExit: 3},
		{name: "survivor holds stdout", tail: "exit 0", holdStdout: true, wantExit: 0},
		{
			name: "timeout", tail: "exec sleep 30", wantExit: -1,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 200*time.Millisecond)
			},
		},
		{
			name: "cancel", tail: "exec sleep 30", wantExit: -1,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(200*time.Millisecond, cancel)
				return ctx, cancel
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			redirect := " >/dev/null 2>&1"
			if tc.holdStdout {
				redirect = ""
			}
			script := fmt.Sprintf("sleep 30%s &\necho $! > %q\n%s\n", redirect, pidFile, tc.tail)
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tc.ctx != nil {
				ctx, cancel = tc.ctx()
			}
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
			Prepare(cmd)
			cmd.WaitDelay = 10 * time.Second
			var stdout bytes.Buffer
			cmd.Stdout = &stdout

			start := time.Now()
			err := Run(cmd)
			elapsed := time.Since(start)

			if errors.Is(err, exec.ErrWaitDelay) || elapsed > 5*time.Second {
				t.Errorf("Run = %v after %s; a survivor held it", err, elapsed)
			}
			if tc.wantExit >= 0 {
				var exitErr *exec.ExitError
				switch {
				case tc.wantExit == 0 && err != nil:
					t.Errorf("Run = %v, want nil", err)
				case tc.wantExit != 0 && !errors.As(err, &exitErr):
					t.Errorf("Run = %v, want an *exec.ExitError", err)
				}
				if got := cmd.ProcessState.ExitCode(); got != tc.wantExit {
					t.Errorf("exit code = %d, want %d", got, tc.wantExit)
				}
			} else if err == nil {
				t.Errorf("Run = nil, want the error ctx caused")
			}
			child := readPid(t, pidFile)
			if !gone(child, 3*time.Second) {
				t.Errorf("the backgrounded child %d outlived Run", child)
			}
		})
	}
}

// TestStartRefusesACommandOutsideItsOwnGroup: without Setpgid the group kill
// would reach the caller's own group, so Start refuses and starts nothing.
func TestStartRefusesACommandOutsideItsOwnGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if _, err := Start(cmd); !errors.Is(err, errNoProcessGroup) {
		t.Fatalf("Start = %v, want errNoProcessGroup", err)
	}
	if cmd.Process != nil {
		t.Errorf("Start started a command it refused")
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// gone polls until pid no longer runs (gone, or a zombie waiting to be
// reaped), or within gives up.
func gone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
