package ipc

import (
	"context"
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// The daemon's `autonomous.start` is the one entry point that starts a
// workspace's autonomous scheduler from outside the daemon: the extension's
// Start button, `nightgauge autonomous start` and doctor's automation.restart
// remedy (#2090) all reach it here.

// daemonDialTimeout is how long a caller waits for the local socket; no
// daemon means ENOENT or ECONNREFUSED near-instantly.
const daemonDialTimeout = 300 * time.Millisecond

// NoDaemonError reports that no daemon is listening for a workspace.
type NoDaemonError struct {
	WorkspaceRoot string
	Err           error
}

func (e *NoDaemonError) Error() string {
	return fmt.Sprintf("no daemon is listening in %s: %v", e.WorkspaceRoot, e.Err)
}

func (e *NoDaemonError) Unwrap() error { return e.Err }

// StartDaemonScheduler asks the daemon serving workspaceRoot to start its
// autonomous scheduler and returns the status the daemon observed. A missing
// daemon is a *NoDaemonError.
func StartDaemonScheduler(ctx context.Context, workspaceRoot string) (orchestrator.AutonomousState, error) {
	var status orchestrator.AutonomousState
	client, err := DialDaemon(ctx, workspaceRoot, daemonDialTimeout)
	if err != nil {
		return status, &NoDaemonError{WorkspaceRoot: workspaceRoot, Err: err}
	}
	defer client.Close()
	err = client.Call(ctx, "autonomous.start", AutonomousStartParams{}, &status)
	return status, err
}

// daemonAutonomousStarter is doctor's view of the same entry point.
type daemonAutonomousStarter struct{}

// Reachable asks the daemon for its scheduler's status, which starts nothing
// and fails when no daemon listens or none has a scheduler attached.
func (daemonAutonomousStarter) Reachable(ctx context.Context, workspaceRoot string) error {
	client, err := DialDaemon(ctx, workspaceRoot, daemonDialTimeout)
	if err != nil {
		return &NoDaemonError{WorkspaceRoot: workspaceRoot, Err: err}
	}
	defer client.Close()
	var status orchestrator.AutonomousState
	if err := client.Call(ctx, "autonomous.status", nil, &status); err != nil {
		return fmt.Errorf("the daemon has no autonomous scheduler to start: %w", err)
	}
	return nil
}

// Start is StartDaemonScheduler.
func (daemonAutonomousStarter) Start(ctx context.Context, workspaceRoot string) error {
	_, err := StartDaemonScheduler(ctx, workspaceRoot)
	return err
}

func init() {
	doctor.RegisterAutonomousStarter(daemonAutonomousStarter{})
}
