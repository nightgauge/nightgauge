package orchestrator

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// defaultCaffeinatePath is where macOS ships caffeinate(8).
const defaultCaffeinatePath = "/usr/bin/caffeinate"

// sleepAssertionStopWait bounds how long Stop waits for the child to exit
// after it is killed.
const sleepAssertionStopWait = 5 * time.Second

// SleepAssertionStatus is the sleep assertion's state as `nightgauge
// autonomous status` reports it (#3136).
type SleepAssertionStatus struct {
	// Held is true while a caffeinate child is alive.
	Held bool `json:"held"`
	// PID is the caffeinate child's pid while Held.
	PID int `json:"pid,omitempty"`
	// Reason says why the assertion is not held ("not darwin", a spawn
	// error, "scheduler not running"). Empty while Held.
	Reason string `json:"reason,omitempty"`
}

// SleepAssertion keeps a macOS host awake for as long as the autonomous
// scheduler runs (#3136, ADR-003). On darwin, Start spawns
// `caffeinate -i -s -w <pid>`: -i holds PreventUserIdleSystemSleep, -s holds
// PreventSystemSleep on AC power, and -w ends the child when the daemon (pid)
// exits, so a crashed daemon cannot leave the host held awake. Stop kills the
// child it spawned and waits for it. On every other platform both are no-ops,
// logged once.
//
// The zero value is not usable; build one with NewSleepAssertion.
type SleepAssertion struct {
	goos     string
	absPath  string
	lookPath func(string) (string, error)

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	reason string

	unsupportedOnce sync.Once
}

// NewSleepAssertion returns an assertion for the running platform.
func NewSleepAssertion() *SleepAssertion {
	return &SleepAssertion{
		goos:     runtime.GOOS,
		absPath:  defaultCaffeinatePath,
		lookPath: exec.LookPath,
		reason:   "scheduler not running",
	}
}

// Start spawns the caffeinate child for pid. Calling Start while the child is
// alive is a no-op. A failure to spawn is returned and recorded in Status; it
// never stops the scheduler.
func (a *SleepAssertion) Start(pid int) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil {
		return nil
	}
	if a.goos != "darwin" {
		a.reason = fmt.Sprintf("not darwin (%s): no sleep assertion is needed or available", a.goos)
		a.unsupportedOnce.Do(func() {
			log.Printf("autonomous: sleep assertion skipped — %s", a.reason)
		})
		return nil
	}
	bin, err := a.resolve()
	if err != nil {
		a.reason = err.Error()
		return err
	}
	cmd := exec.Command(bin, "-i", "-s", "-w", strconv.Itoa(pid))
	if err := cmd.Start(); err != nil {
		a.reason = fmt.Sprintf("caffeinate did not start: %v", err)
		return fmt.Errorf("sleep assertion: %w", err)
	}
	done := make(chan struct{})
	a.cmd, a.done, a.reason = cmd, done, ""
	childPID := cmd.Process.Pid
	log.Printf("autonomous: sleep assertion held — caffeinate pid %d (-i -s -w %d)", childPID, pid)
	go func() {
		_ = cmd.Wait()
		close(done)
		a.mu.Lock()
		if a.cmd == cmd {
			// Exited on its own (killed from outside, or -w saw pid exit).
			a.cmd, a.done = nil, nil
			a.reason = fmt.Sprintf("caffeinate pid %d exited", childPID)
		}
		a.mu.Unlock()
	}()
	return nil
}

// resolve finds caffeinate: the absolute path first, then PATH.
func (a *SleepAssertion) resolve() (string, error) {
	if a.absPath != "" {
		if info, err := os.Stat(a.absPath); err == nil && !info.IsDir() {
			return a.absPath, nil
		}
	}
	if a.lookPath != nil {
		if p, err := a.lookPath("caffeinate"); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("caffeinate not found at %s or on PATH", a.absPath)
}

// Stop kills the caffeinate child Start spawned and waits for it to exit.
// Safe to call when nothing is held.
func (a *SleepAssertion) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	cmd, done := a.cmd, a.done
	a.cmd, a.done = nil, nil
	if a.goos == "darwin" || cmd != nil {
		a.reason = "scheduler not running"
	}
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	select {
	case <-done:
		log.Printf("autonomous: sleep assertion released — caffeinate pid %d stopped", pid)
	case <-time.After(sleepAssertionStopWait):
		log.Printf("autonomous: sleep assertion — caffeinate pid %d did not exit within %s of kill", pid, sleepAssertionStopWait)
	}
}

// Status reports whether the assertion is held, and its pid.
func (a *SleepAssertion) Status() SleepAssertionStatus {
	if a == nil {
		return SleepAssertionStatus{Reason: "no sleep assertion configured"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil && a.cmd.Process != nil {
		return SleepAssertionStatus{Held: true, PID: a.cmd.Process.Pid}
	}
	return SleepAssertionStatus{Reason: a.reason}
}
