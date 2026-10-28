package gates

import (
	"context"
	"time"
)

// Stage start (#2637): the moment the scheduler dispatched the stage whose
// post-conditions a gate is judging. A gate that reads a context file uses it
// to tell a file this attempt wrote from one a prior attempt left behind.

type stageStartKey struct{}

// WithStageStart returns a context carrying the stage's dispatch time. The
// scheduler sets it before invoking a gate's Verify; gates read it via
// StageStart.
func WithStageStart(ctx context.Context, start time.Time) context.Context {
	return context.WithValue(ctx, stageStartKey{}, start)
}

// StageStart reports the dispatch time of the stage being judged. ok is false
// when no start was recorded (the `gate verify` CLI, most tests): gates then
// skip any freshness check.
func StageStart(ctx context.Context) (time.Time, bool) {
	v, ok := ctx.Value(stageStartKey{}).(time.Time)
	return v, ok && !v.IsZero()
}
