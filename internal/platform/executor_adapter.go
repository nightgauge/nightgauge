package platform

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nightgauge/nightgauge/internal/executor"
)

// ExecutorAdapter adapts executor.CommandExecutor to platform.CommandExecutor.
// It translates each PendingCommand into an executor.Command, dispatches it,
// and acknowledges the result via the provided ack function.
type ExecutorAdapter struct {
	exec  *executor.CommandExecutor
	ackFn func(ctx context.Context, cmdID string, result CommandResult) error
}

// NewExecutorAdapter creates an adapter wrapping the given executor.
// ackFn is called after every command execution to post the result back to
// the platform. Pass nil to skip acknowledgement (e.g., in tests).
func NewExecutorAdapter(
	exec *executor.CommandExecutor,
	ackFn func(ctx context.Context, cmdID string, result CommandResult) error,
) *ExecutorAdapter {
	return &ExecutorAdapter{exec: exec, ackFn: ackFn}
}

// Execute satisfies the platform.CommandExecutor interface.
// It converts the PendingCommand to an executor.Command, dispatches it, and
// acknowledges the result. Acknowledgement errors are ignored (fire-and-forget).
func (a *ExecutorAdapter) Execute(ctx context.Context, cmd PendingCommand) error {
	start := time.Now()

	result, err := a.exec.Execute(ctx, executor.Command{
		Type:    executor.CommandType(cmd.Type),
		Payload: json.RawMessage(cmd.Payload),
	})

	durationMs := time.Since(start).Milliseconds()

	ackResult := CommandResult{DurationMs: durationMs}
	if err != nil {
		ackResult.Status = "failure"
		ackResult.Error = err.Error()
	} else if result != nil && result.Error != "" {
		ackResult.Status = "failure"
		ackResult.Error = result.Error
	} else {
		ackResult.Status = "success"
		if result != nil && result.Data != nil {
			out, jsonErr := json.Marshal(result.Data)
			if jsonErr == nil {
				ackResult.Output = string(out)
			}
		}
	}

	if a.ackFn != nil {
		_ = a.ackFn(ctx, cmd.ID, ackResult)
	}
	return nil
}
