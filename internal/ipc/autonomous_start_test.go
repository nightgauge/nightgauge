package ipc

import (
	"context"
	"errors"
	"testing"
)

// With no daemon listening, the shared entry point says so as a
// *NoDaemonError, and doctor's reachability probe refuses without starting
// anything (#2090).
func TestStartDaemonScheduler_NoDaemon(t *testing.T) {
	root := t.TempDir()
	_, err := StartDaemonScheduler(context.Background(), root)
	var nd *NoDaemonError
	if !errors.As(err, &nd) || nd.WorkspaceRoot != root {
		t.Fatalf("err = %v, want a *NoDaemonError for %s", err, root)
	}
	if err := (daemonAutonomousStarter{}).Reachable(context.Background(), root); !errors.As(err, &nd) {
		t.Fatalf("Reachable = %v, want a *NoDaemonError", err)
	}
}
