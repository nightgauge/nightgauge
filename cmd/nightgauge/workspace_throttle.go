package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// The platform's workspace throttle outside the extension (#2352).
//
// The daemon follows the throttle of the workspace it serves, read from the
// platform's agent throttle endpoint under its registered agent
// (runDaemonPlatformAgent). A headless scheduler (`autonomous run`,
// `pipeline run --auto`) registers no agent of its own, so it asks the
// workspace's daemon instead. With no daemon, or a daemon that follows no
// throttle (one not connected to the platform), nothing is applied.

// throttleCommandType is the platform's workspace throttle command (#2337).
const throttleCommandType = "throttle"

// daemonThrottleInterval is how often a headless scheduler asks the
// workspace's daemon for the throttle: one local socket round-trip, at the
// autonomous scheduler's default scan interval.
const daemonThrottleInterval = 30 * time.Second

// refreshThrottleOnCommand wraps relay so a `throttle` command the daemon's
// agent receives makes the daemon read its workspace throttle again (#2352),
// and is then relayed to the extension as before. The command names no
// workspace, so its payload is never applied.
func refreshThrottleOnCommand(relay platform.AgentCommandRelay, refresh func()) platform.AgentCommandRelay {
	return func(agentID string, cmd platform.PendingCommand) {
		if cmd.Type == throttleCommandType {
			go refresh()
		}
		relay(agentID, cmd)
	}
}

// readDaemonWorkspaceThrottle asks the daemon serving workspaceRoot for the
// throttle it follows, over the workspace socket.
func readDaemonWorkspaceThrottle(ctx context.Context, workspaceRoot string) (ipc.PlatformWorkspaceThrottleResult, error) {
	var result ipc.PlatformWorkspaceThrottleResult
	c, err := ipc.DialDaemon(ctx, workspaceRoot, daemonDialTimeout)
	if err != nil {
		return result, err
	}
	defer c.Close()
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = c.Call(callCtx, "platform.workspaceThrottle", nil, &result)
	return result, err
}

// daemonThrottleFollower keeps a headless scheduler's dispatch throttle on
// the workspace throttle the workspace's daemon follows (#2352).
type daemonThrottleFollower struct {
	throttle *orchestrator.DispatchThrottle
	read     func(ctx context.Context) (ipc.PlatformWorkspaceThrottleResult, error)
	last     string
}

// step asks the daemon once and applies its answer. Only an answer that names
// the throttle, or says the daemon follows none, changes what is applied. A
// daemon that cannot be reached, or that follows the throttle but has not
// read it yet (not registered yet, or its reads failing), changes nothing: a
// throttle it reported before is kept, until its resumeAt or until a daemon
// reports the workspace's throttle again, and the log says so; with none
// learned, nothing is capped. Each change of state is logged once.
func (f *daemonThrottleFollower) step(ctx context.Context) {
	result, err := f.read(ctx)
	switch {
	case err != nil:
		if kept, _ := f.throttle.Snapshot(); kept != nil {
			f.report("kept "+describeWorkspaceThrottle(kept),
				"the workspace's daemon cannot be reached, so the last throttle it reported is kept: %s (%v)",
				describeWorkspaceThrottle(kept), err)
		} else {
			f.report("unreachable", "no daemon serves this workspace, so the platform's throttle is not followed (%v)", err)
		}
	case result.Known && result.Throttle == nil:
		f.throttle.Set(nil, true)
		f.report("none", "the workspace has no throttle")
	case result.Known:
		f.throttle.Set(result.Throttle, true)
		f.report("throttled "+describeWorkspaceThrottle(result.Throttle), "the workspace is throttled to %s", describeWorkspaceThrottle(result.Throttle))
	case result.Unread:
		if kept, _ := f.throttle.Snapshot(); kept != nil {
			f.report("unread, kept "+describeWorkspaceThrottle(kept),
				"the workspace's daemon has not read the platform's throttle yet, so the last throttle it reported is kept: %s",
				describeWorkspaceThrottle(kept))
		} else {
			f.report("unread", "the workspace's daemon has not read the platform's throttle yet, so none is applied yet")
		}
	default:
		f.throttle.Set(nil, false)
		f.report("unknown", "the workspace's daemon follows no throttle (it is not connected to the platform), so the platform's throttle is not followed")
	}
}

func (f *daemonThrottleFollower) report(state string, format string, args ...interface{}) {
	if state == f.last {
		return
	}
	f.last = state
	log.Printf("[nightgauge] workspace throttle: "+format, args...)
}

// loop asks the daemon every interval until ctx ends.
func (f *daemonThrottleFollower) loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		f.step(ctx)
	}
}

// describeWorkspaceThrottle names a throttle for the log: its cap and until when.
func describeWorkspaceThrottle(t *platform.WorkspaceThrottle) string {
	until := "until the platform clears it"
	if t.ResumeAt != nil {
		until = "until " + t.ResumeAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%d run(s) at once %s", t.MaxConcurrent, until)
}

// followDaemonWorkspaceThrottle keeps throttle on the workspace throttle the
// workspace's daemon follows (#2352): now, then every interval, until ctx
// ends (see daemonThrottleFollower.step).
func followDaemonWorkspaceThrottle(
	ctx context.Context,
	throttle *orchestrator.DispatchThrottle,
	interval time.Duration,
	read func(ctx context.Context) (ipc.PlatformWorkspaceThrottleResult, error),
) {
	f := &daemonThrottleFollower{throttle: throttle, read: read}
	f.step(ctx)
	f.loop(ctx, interval)
}

// startFollowingDaemonWorkspaceThrottle asks the workspace's daemon for the
// throttle before it returns, so a scheduler started next already holds its
// first dispatch to it, then keeps following it every interval in the
// background until ctx ends (#2352).
func startFollowingDaemonWorkspaceThrottle(
	ctx context.Context,
	throttle *orchestrator.DispatchThrottle,
	interval time.Duration,
	read func(ctx context.Context) (ipc.PlatformWorkspaceThrottleResult, error),
) {
	f := &daemonThrottleFollower{throttle: throttle, read: read}
	f.step(ctx)
	go f.loop(ctx, interval)
}
