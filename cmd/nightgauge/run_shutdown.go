package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// stageShutdownGrace is how long each stage gets to exit on SIGTERM when
// `nightgauge run` is interrupted, before its process group is SIGKILLed.
const stageShutdownGrace = 5 * time.Second

// withStageShutdown returns a context that SIGINT and SIGTERM cancel, and a
// shutdown to defer that stops every live stage's process group and waits for
// each to be reaped (#2171).
//
// Stages run in their own process group, so a terminal's Ctrl-C reaches only
// this process. With no handler Go's default disposition exited at once and
// left the adapter child (`opencode`) running, reparented to PID 1. Now the
// signal cancels the run's context, which the stage context derives from, and
// shutdown waits for the stages before the command returns. RunAuto dispatches
// pipelines on goroutines it does not wait for, so the wait is needed even
// though cancelling the context already kills each group.
func withStageShutdown(parent context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	return ctx, func() {
		execution.StopAllStages(stageShutdownGrace)
		stop()
	}
}
