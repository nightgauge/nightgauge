package orchestrator

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubCaffeinate puts a `caffeinate` on PATH that records its argv and then
// waits, so the test can see what the assertion spawned and kill it.
func stubCaffeinate(t *testing.T) (argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	script := "#!/bin/sh\necho \"$@\" > '" + argvFile + "'\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(dir, "caffeinate"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvFile
}

func TestSleepAssertionHoldsCaffeinateForThePid(t *testing.T) {
	argvFile := stubCaffeinate(t)
	a := &SleepAssertion{goos: "darwin", absPath: filepath.Join(t.TempDir(), "missing"), lookPath: exec.LookPath}
	daemonPID := os.Getpid()

	if err := a.Start(daemonPID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := a.Status()
	if !st.Held || st.PID == 0 {
		t.Fatalf("Status = %+v, want held with a pid", st)
	}
	childPID := st.PID

	var argv string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(argvFile); err == nil && len(b) > 0 {
			argv = strings.TrimSpace(string(b))
			break
		}
	}
	if want := "-i -s -w " + strconv.Itoa(daemonPID); argv != want {
		t.Fatalf("caffeinate argv = %q, want %q", argv, want)
	}

	a.Stop()
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("caffeinate pid %d still exists after Stop (kill 0: %v)", childPID, err)
	}
	if st := a.Status(); st.Held || st.Reason == "" {
		t.Errorf("Status after Stop = %+v, want not held with a reason", st)
	}
}

func TestSleepAssertionIsANoOpOffDarwin(t *testing.T) {
	argvFile := stubCaffeinate(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	a := &SleepAssertion{goos: "linux", absPath: defaultCaffeinatePath, lookPath: exec.LookPath}
	for i := 0; i < 2; i++ {
		if err := a.Start(os.Getpid()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		a.Stop()
	}
	if st := a.Status(); st.Held || !strings.Contains(st.Reason, "not darwin") {
		t.Errorf("Status = %+v, want not held, not darwin", st)
	}
	if _, err := os.Stat(argvFile); !os.IsNotExist(err) {
		t.Error("caffeinate was spawned off darwin")
	}
	if n := strings.Count(buf.String(), "sleep assertion skipped"); n != 1 {
		t.Errorf("skip logged %d times across two Starts, want once: %q", n, buf.String())
	}
}

func TestSleepAssertionReportsAMissingCaffeinate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	a := &SleepAssertion{goos: "darwin", absPath: filepath.Join(t.TempDir(), "missing"), lookPath: exec.LookPath}
	if err := a.Start(os.Getpid()); err == nil {
		t.Fatal("Start succeeded with no caffeinate anywhere")
	}
	if st := a.Status(); st.Held || !strings.Contains(st.Reason, "caffeinate not found") {
		t.Errorf("Status = %+v, want the missing binary named", st)
	}
}
