package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// Issue #274: `autonomous status` must detect a "running"/"paused" state
// whose recorded writer PID is dead and report it as stalled, rather than
// blindly trusting a possibly-stale on-disk status.
func TestAutonomousStateIsStalled_LiveVsDeadPID(t *testing.T) {
	// A dead PID: spawn a trivial child process and wait for it to exit.
	cmd := exec.Command(os.Args[0], "-test.run=NONE")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn helper process: %v", err)
	}
	deadPID := cmd.Process.Pid

	tests := []struct {
		name  string
		state orchestrator.AutonomousState
		want  bool
	}{
		{
			name:  "running with live pid is not stalled",
			state: orchestrator.AutonomousState{Status: "running", PID: os.Getpid()},
			want:  false,
		},
		{
			name:  "paused with live pid is not stalled",
			state: orchestrator.AutonomousState{Status: "paused", PID: os.Getpid()},
			want:  false,
		},
		{
			name:  "running with dead pid is stalled",
			state: orchestrator.AutonomousState{Status: "running", PID: deadPID},
			want:  true,
		},
		{
			name:  "running with no recorded pid is stalled",
			state: orchestrator.AutonomousState{Status: "running", PID: 0},
			want:  true,
		},
		{
			name:  "stopped with dead pid is not stalled — status is terminal, not live",
			state: orchestrator.AutonomousState{Status: "stopped", PID: deadPID},
			want:  false,
		},
		{
			name:  "complete status is never stalled",
			state: orchestrator.AutonomousState{Status: "complete", PID: 0},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autonomousStateIsStalled(tt.state); got != tt.want {
				t.Errorf("autonomousStateIsStalled(%+v) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}
