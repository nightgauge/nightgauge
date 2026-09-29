package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
)

// The automation.restart verb (#2090). A stopped autonomous loop is restarted
// through the entry point that normally starts it: the running daemon's
// `autonomous.start` method, which the extension's Start button and
// `nightgauge autonomous start` call. Doctor never spawns a daemon and never
// writes the scheduler's state file: there is nothing a file write could
// start, and a detached daemon started from a diagnostic command is a process
// nobody asked to own.
//
// The daemon client lives in internal/ipc, which imports this package, so the
// entry point is registered from there (RegisterAutonomousStarter) rather than
// imported. With nothing registered, or no daemon listening, the check offers
// the manual start steps instead of a remedy --fix would have to refuse.

// AutonomousStarter is the normal entry point for starting a workspace's
// autonomous scheduler.
type AutonomousStarter interface {
	// Reachable reports, without starting anything, whether the entry point
	// can start the scheduler for workspaceRoot now: a daemon is listening
	// and has a scheduler attached.
	Reachable(ctx context.Context, workspaceRoot string) error
	// Start asks the entry point to start the scheduler.
	Start(ctx context.Context, workspaceRoot string) error
}

var (
	starterMu         sync.RWMutex
	autonomousStarter AutonomousStarter
)

// RegisterAutonomousStarter wires the entry point automation.restart uses. The
// daemon client registers itself at init; tests register a fake and restore
// the previous value.
func RegisterAutonomousStarter(s AutonomousStarter) (previous AutonomousStarter) {
	starterMu.Lock()
	defer starterMu.Unlock()
	previous, autonomousStarter = autonomousStarter, s
	return previous
}

func registeredAutonomousStarter() AutonomousStarter {
	starterMu.RLock()
	defer starterMu.RUnlock()
	return autonomousStarter
}

// errNoAutonomousStarter: no entry point is wired into this binary.
var errNoAutonomousStarter = errors.New("no autonomous scheduler entry point is wired into this binary")

// reachProbeTimeout bounds the scan-time reachability probe: a local socket
// answers near-instantly or not at all.
const reachProbeTimeout = 2 * time.Second

// autonomousRestartable reports whether the autonomous loop of workspaceRoot
// can be restarted from doctor now; nil means a confirm restart is offered.
func autonomousRestartable(ctx context.Context, workspaceRoot string) error {
	s := registeredAutonomousStarter()
	if s == nil {
		return errNoAutonomousStarter
	}
	if workspaceRoot == "" {
		return errors.New("no workspace root")
	}
	ctx, cancel := context.WithTimeout(ctx, reachProbeTimeout)
	defer cancel()
	return s.Reachable(ctx, workspaceRoot)
}

// restartWait bounds how long automation.restart waits for the restarted loop
// to record a scan before handing the answer to verification. The daemon's
// first cycle starts at once; a loop that has not scanned by then is reported
// still-present by the re-run check, never assumed started.
var restartWait = 30 * time.Second

// startTimeout bounds the start call itself; the daemon waits for its loop to
// come up before it answers.
const startTimeout = time.Minute

// restartTarget re-derives, from the finding and the workspace, the
// automation the verb would restart. Only the built-in autonomous loop has an
// entry point doctor can reach.
func (v *verbs) restartTarget(f Finding) (cadence.Automation, string, error) {
	if f.Code != codeAutomationStopped {
		return cadence.Automation{}, "", fmt.Errorf("automation.restart acts only on a stopped automation (%s), not %s", codeAutomationStopped, f.Code)
	}
	id, err := evidence(f, "automation")
	if err != nil {
		return cadence.Automation{}, "", err
	}
	if kind := f.Evidence["evidence_kind"]; kind != string(cadence.EvidenceAutonomousState) {
		return cadence.Automation{}, "", fmt.Errorf("automation %s is a %s automation; doctor restarts only the autonomous loop, through the daemon", id, kind)
	}
	a, ok := cadence.ByID(id)
	if !ok || a.Kind != cadence.EvidenceAutonomousState {
		return cadence.Automation{}, "", fmt.Errorf("automation %s is not the built-in autonomous loop", id)
	}
	root := v.env.Cwd
	if root == "" {
		return cadence.Automation{}, "", errors.New("no workspace root")
	}
	return a, root, nil
}

// restartPrecondition re-reads the loop's own evidence: still stopped, with
// the last scan the finding recorded (a scan since means something else
// restarted it), and the entry point still reachable.
func (v *verbs) restartPrecondition(ctx context.Context, f Finding) error {
	a, root, err := v.restartTarget(f)
	if err != nil {
		return err
	}
	ev := autonomousStateEvidence(root)(ctx, a)
	now := v.env.Now
	if now.IsZero() {
		now = time.Now()
	}
	verdict := cadence.Evaluate(a, ev, now, cadence.DefaultStaleMultiple)
	if verdict.Status != cadence.StatusStale {
		return fmt.Errorf("the autonomous loop is no longer stopped (%s): nothing to restart", verdict.Status)
	}
	if want := f.Evidence["last_ran"]; want != "" && ev.Newest.UTC().Format(time.RFC3339) != want {
		return fmt.Errorf("the autonomous loop scanned at %s since doctor's scan (which saw %s)", ev.Newest.UTC().Format(time.RFC3339), want)
	}
	s := registeredAutonomousStarter()
	if s == nil {
		return errNoAutonomousStarter
	}
	if err := s.Reachable(ctx, root); err != nil {
		return fmt.Errorf("the scheduler's entry point cannot be reached: %w", err)
	}
	return nil
}

// restartApply starts the scheduler through the entry point, then waits a
// bounded time for the loop's first scan. The outcome is decided by
// re-running scheduled_automations, never by this return value.
func (v *verbs) restartApply(ctx context.Context, f Finding) error {
	a, root, err := v.restartTarget(f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	s := registeredAutonomousStarter()
	if s == nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, errNoAutonomousStarter)
	}
	scanned := lastRan(autonomousStateEvidence(root)(ctx, a))
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := s.Start(sctx, root); err != nil {
		return fmt.Errorf("start the autonomous scheduler: %w", err)
	}
	deadline := time.Now().Add(restartWait)
	for time.Now().Before(deadline) {
		if now := lastRan(autonomousStateEvidence(root)(ctx, a)); now.After(scanned) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// lastRan is the evidence's last-run time, zero when it has none.
func lastRan(e cadence.Evidence) time.Time {
	if e.Err != nil || !e.EverRan {
		return time.Time{}
	}
	return e.Newest
}

// manualStartSteps are the steps offered when doctor cannot reach the entry
// point itself.
func manualStartSteps(reason error) []string {
	steps := []string{
		"Start a daemon for this workspace (`nightgauge serve`, or open the VS Code extension), then run `nightgauge autonomous start`",
		"Or run a scheduler in this process: `nightgauge autonomous run`",
	}
	if reason != nil && !errors.Is(reason, errNoAutonomousStarter) {
		steps = append(steps, "Doctor could not start it itself: "+strings.TrimSpace(reason.Error()))
	}
	return steps
}
