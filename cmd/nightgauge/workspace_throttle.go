package main

import (
	"context"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// The platform's workspace throttle outside the extension (#2352).
//
// The daemon follows the throttle of the workspace it serves while it has a
// signed-in session (runDaemonPlatformAgent). A headless scheduler
// (`autonomous run`, `pipeline run --auto`) holds only a license key, and the
// platform answers the workspace list only to a signed-in user, so it asks
// the workspace's daemon instead. With no daemon, or a daemon that follows no
// throttle, nothing is applied: the same rule the extension keeps, which
// follows the throttle only while a session exists.

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

// followDaemonWorkspaceThrottle keeps throttle on the workspace throttle the
// workspace's daemon follows (#2352): now, then every interval, until ctx
// ends. A daemon that cannot be reached changes nothing, so a throttle
// already learned holds until its resumeAt; one that follows no throttle
// lifts it. Each change of state is logged once.
func followDaemonWorkspaceThrottle(
	ctx context.Context,
	throttle *orchestrator.DispatchThrottle,
	interval time.Duration,
	read func(ctx context.Context) (ipc.PlatformWorkspaceThrottleResult, error),
) {
	last := ""
	report := func(state string, format string, args ...interface{}) {
		if state == last {
			return
		}
		last = state
		log.Printf("[nightgauge] workspace throttle: "+format, args...)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, err := read(ctx)
		switch {
		case err != nil:
			report("unreachable", "no daemon serves this workspace with a signed-in session, so the platform's throttle is not followed (%v)", err)
		case !result.Known:
			throttle.Set(nil, false)
			report("unknown", "the workspace's daemon has no signed-in session, so the platform's throttle is not followed")
		case result.Throttle == nil:
			throttle.Set(nil, true)
			report("none", "the workspace has no throttle")
		default:
			throttle.Set(result.Throttle, true)
			report("throttled", "the workspace is throttled to %d run(s) at once", result.Throttle.MaxConcurrent)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
