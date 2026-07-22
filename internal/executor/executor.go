// Package executor implements type-based command dispatch for remote commands
// polled from the platform API. It routes each command to its registered handler,
// enforces a per-command execution timeout, and returns a structured result.
// Unknown command types return an error result — they never panic.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is the per-command execution timeout used when neither the
// Command nor the CommandExecutor specifies one.
const DefaultTimeout = 5 * time.Minute

// CommandType is a typed string key used for handler dispatch.
type CommandType string

const (
	CommandTypePipelineRun    CommandType = "pipeline.run"
	CommandTypePipelineCancel CommandType = "pipeline.cancel"
	CommandTypePipelineStatus CommandType = "pipeline.status"
	CommandTypeConfigReload   CommandType = "config.reload"
)

// Handler is the function signature all command handlers must satisfy.
type Handler func(ctx context.Context, payload json.RawMessage) (interface{}, error)

// Command is a command to be dispatched by the executor.
type Command struct {
	Type    CommandType
	Payload json.RawMessage
	// Timeout overrides the executor's default for this command.
	// Zero means use the executor default.
	Timeout time.Duration
}

// Result is the outcome of a dispatched command.
type Result struct {
	Type  CommandType
	Data  interface{}
	Error string // empty on success
}

// CommandExecutor dispatches commands by type with timeout enforcement.
// All methods are safe for concurrent use.
type CommandExecutor struct {
	mu       sync.RWMutex
	handlers map[CommandType]Handler
	timeout  time.Duration
	history  *historyStore
}

// New creates a CommandExecutor with the given default timeout.
// If timeout is 0, DefaultTimeout (5m) is used.
func New(timeout time.Duration) *CommandExecutor {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &CommandExecutor{
		handlers: make(map[CommandType]Handler),
		timeout:  timeout,
		history:  newHistoryStore(),
	}
}

// Register adds a handler for a command type.
// Overwrites any existing handler for the same type.
func (e *CommandExecutor) Register(t CommandType, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[t] = h
}

// Execute dispatches a command to its registered handler with timeout enforcement.
// Unknown command types return a Result with Error set — Execute never returns a
// Go error for unknown command types.
func (e *CommandExecutor) Execute(ctx context.Context, cmd Command) (*Result, error) {
	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = e.timeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	e.mu.RLock()
	handler, ok := e.handlers[cmd.Type]
	e.mu.RUnlock()

	receivedAt := time.Now()

	if !ok {
		errMsg := fmt.Sprintf("unknown command type: %s", cmd.Type)
		completedAt := time.Now()
		durationMs := completedAt.Sub(receivedAt).Milliseconds()
		e.history.add(CommandHistoryEntry{
			ID:          fmt.Sprintf("%s-%d", cmd.Type, receivedAt.UnixNano()),
			Type:        string(cmd.Type),
			Status:      "failure",
			ReceivedAt:  receivedAt,
			CompletedAt: &completedAt,
			DurationMs:  durationMs,
			Error:       errMsg,
		})
		return &Result{
			Type:  cmd.Type,
			Error: errMsg,
		}, nil
	}

	data, err := handler(execCtx, cmd.Payload)
	completedAt := time.Now()
	durationMs := completedAt.Sub(receivedAt).Milliseconds()
	result := &Result{Type: cmd.Type, Data: data}
	entry := CommandHistoryEntry{
		ID:          fmt.Sprintf("%s-%d", cmd.Type, receivedAt.UnixNano()),
		Type:        string(cmd.Type),
		ReceivedAt:  receivedAt,
		CompletedAt: &completedAt,
		DurationMs:  durationMs,
	}
	if err != nil {
		result.Error = err.Error()
		entry.Status = "failure"
		entry.Error = err.Error()
	} else {
		entry.Status = "success"
	}
	e.history.add(entry)
	return result, nil
}

// GetCommandHistory returns a snapshot of recent command executions (oldest-first).
func (e *CommandExecutor) GetCommandHistory() []CommandHistoryEntry {
	return e.history.getAll()
}

// GetPollingStatus returns the current remote-command polling state.
func (e *CommandExecutor) GetPollingStatus() CommandPollingStatus {
	return e.history.getPollingStatus()
}

// SetPollingStatus updates the polling state stored in the history store.
// Called by the CommandPoller to reflect current activity.
func (e *CommandExecutor) SetPollingStatus(active bool, lastPolledAt *time.Time, pendingCount, errorCount int) {
	e.history.setPollingStatus(active, lastPolledAt, pendingCount, errorCount)
}

// SchedulerIface is the minimal interface required by the pipeline.run handler.
type SchedulerIface interface {
	QueueAdd(entries ...QueueEntry)
}

// QueueEntry mirrors orchestrator.QueueEntry to avoid importing that package.
// The executor package stays decoupled from orchestrator to prevent import cycles.
type QueueEntry struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Priority    int    `json:"priority"`
	// RemoteRunID carries the platform-assigned run ID from remote-triggered commands (#3557).
	RemoteRunID string `json:"remoteRunId,omitempty"`
}

// ExecManagerIface is the minimal interface required by the pipeline.cancel handler.
type ExecManagerIface interface {
	Stop(key string)
	// CancelWithGrace sends SIGTERM to the execution keyed by key, waits up to
	// timeout for a graceful exit, then sends SIGKILL if the process is still
	// running. It also calls the execution's context cancel function.
	// Returns (true, nil) if the process exited within the grace period,
	// (false, nil) if force-killed, and (false, nil) if no execution was found.
	CancelWithGrace(key string, timeout time.Duration) (bool, error)
}

// StateServiceIface is the minimal interface required by the pipeline.status handler.
type StateServiceIface interface {
	GetState(key string) interface{}
}

// IssueGetterIface is the minimal interface required by pipeline.run for issue
// pre-flight validation. Returns a non-nil value when the issue exists, nil when
// not found. Using interface{} mirrors StateServiceIface.GetState to keep the
// executor package decoupled from the types package.
type IssueGetterIface interface {
	GetIssue(ctx context.Context, owner, repo string, number int) (interface{}, error)
}

// Deps holds the dependencies required by the built-in command handlers.
type Deps struct {
	Scheduler SchedulerIface
	ExecMgr   ExecManagerIface
	StateSvc  StateServiceIface
	// IssueGetter validates issue existence before pipeline.run queues the issue.
	// When nil, validation is skipped (backward-compatible default).
	IssueGetter IssueGetterIface
	// ConfigLoad loads configuration for the given workspace root.
	// Signature matches config.Load — injected as a func to avoid importing config.
	ConfigLoad func(root string) (interface{}, error)
	WorkRoot   string
}

// NewWithHandlers returns a CommandExecutor pre-registered with all standard
// command handlers. The polling loop can use it immediately after construction.
func NewWithHandlers(deps Deps) *CommandExecutor {
	e := New(DefaultTimeout)
	e.Register(CommandTypePipelineRun, makePipelineRunHandler(deps.Scheduler, deps.IssueGetter))
	e.Register(CommandTypePipelineCancel, makePipelineCancelHandler(deps.ExecMgr))
	e.Register(CommandTypePipelineStatus, makePipelineStatusHandler(deps.StateSvc))
	e.Register(CommandTypeConfigReload, makeConfigReloadHandler(deps.ConfigLoad, deps.WorkRoot))
	return e
}

// UpdateDeps re-registers all standard command handlers with the provided deps.
// Call this after lazy-initializing dependencies (e.g., when scheduler becomes available).
func (e *CommandExecutor) UpdateDeps(deps Deps) {
	e.Register(CommandTypePipelineRun, makePipelineRunHandler(deps.Scheduler, deps.IssueGetter))
	e.Register(CommandTypePipelineCancel, makePipelineCancelHandler(deps.ExecMgr))
	e.Register(CommandTypePipelineStatus, makePipelineStatusHandler(deps.StateSvc))
	e.Register(CommandTypeConfigReload, makeConfigReloadHandler(deps.ConfigLoad, deps.WorkRoot))
}

// --- Handler implementations ---

// pipelineRunParams are the expected fields for a pipeline.run command payload.
type pipelineRunParams struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	// RunID is the platform-assigned run ID for remote-triggered runs (#3557).
	// When set, the scheduler prefers this over the locally-generated runstate UUID.
	RunID string `json:"run_id,omitempty"`
}

func makePipelineRunHandler(sched SchedulerIface, getter IssueGetterIface) Handler {
	return func(ctx context.Context, payload json.RawMessage) (interface{}, error) {
		if sched == nil {
			return nil, fmt.Errorf("pipeline.run: scheduler not configured")
		}
		var p pipelineRunParams
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("pipeline.run: invalid payload: %w", err)
		}
		if p.Repo == "" {
			return nil, fmt.Errorf("pipeline.run: repo is required")
		}
		if p.IssueNumber <= 0 {
			return nil, fmt.Errorf("pipeline.run: issueNumber must be positive")
		}
		// Validate issue exists when a getter is configured and owner can be resolved.
		if getter != nil {
			owner, repoName := splitOwnerRepoFromParams(p.Owner, p.Repo)
			if owner != "" {
				issue, err := getter.GetIssue(ctx, owner, repoName, p.IssueNumber)
				if err != nil {
					return nil, fmt.Errorf("pipeline.run: failed to verify issue #%d: %w", p.IssueNumber, err)
				}
				if issue == nil {
					return nil, fmt.Errorf("pipeline.run: issue #%d not found in %s/%s", p.IssueNumber, owner, repoName)
				}
			}
		}
		sched.QueueAdd(QueueEntry{
			Repo:        p.Repo,
			IssueNumber: p.IssueNumber,
			RemoteRunID: p.RunID,
		})
		return map[string]interface{}{
			"status": "queued",
			"repo":   p.Repo,
			"issue":  p.IssueNumber,
		}, nil
	}
}

// splitOwnerRepoFromParams resolves owner and repo from pipeline.run payload fields.
// Explicit owner takes priority; if absent, parses "owner/repo" format from repo.
// Returns ("", repo) when owner cannot be determined — callers should skip validation.
func splitOwnerRepoFromParams(owner, repo string) (string, string) {
	if owner != "" {
		return owner, repo
	}
	if idx := strings.IndexByte(repo, '/'); idx >= 0 {
		return repo[:idx], repo[idx+1:]
	}
	return "", repo
}

// cancelGracePeriod is the time the pipeline.cancel handler waits for a graceful
// SIGTERM exit before sending SIGKILL.
const cancelGracePeriod = 30 * time.Second

// pipelineCancelParams are the expected fields for a pipeline.cancel command payload.
type pipelineCancelParams struct {
	ExecutionID string `json:"executionId"`
}

func makePipelineCancelHandler(mgr ExecManagerIface) Handler {
	return func(ctx context.Context, payload json.RawMessage) (interface{}, error) {
		if mgr == nil {
			return nil, fmt.Errorf("pipeline.cancel: execution manager not configured")
		}
		var p pipelineCancelParams
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("pipeline.cancel: invalid payload: %w", err)
		}
		if p.ExecutionID == "" {
			return nil, fmt.Errorf("pipeline.cancel: executionId is required")
		}
		graceful, err := mgr.CancelWithGrace(p.ExecutionID, cancelGracePeriod)
		if err != nil {
			return nil, fmt.Errorf("pipeline.cancel: %w", err)
		}
		return map[string]interface{}{
			"status":      "ok",
			"executionId": p.ExecutionID,
			"graceful":    graceful,
		}, nil
	}
}

// pipelineStatusParams are the expected fields for a pipeline.status command payload.
type pipelineStatusParams struct {
	ExecutionID string `json:"executionId"`           // kept for backward compat
	IssueNumber int    `json:"issueNumber,omitempty"` // preferred over executionId
}

func makePipelineStatusHandler(svc StateServiceIface) Handler {
	return func(ctx context.Context, payload json.RawMessage) (interface{}, error) {
		if svc == nil {
			return nil, fmt.Errorf("pipeline.status: state service not configured")
		}
		var p pipelineStatusParams
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("pipeline.status: invalid payload: %w", err)
		}
		// Derive lookup key: prefer issueNumber field; fall back to executionId string.
		key := p.ExecutionID
		if p.IssueNumber > 0 {
			key = fmt.Sprintf("%d", p.IssueNumber)
		}
		if key == "" {
			return nil, fmt.Errorf("pipeline.status: executionId or issueNumber is required")
		}
		st := svc.GetState(key)
		if st == nil {
			return map[string]interface{}{
				"executionId": key,
				"status":      "idle",
			}, nil
		}
		// st is expected to be map[string]interface{} with status/stage/startedAt/issueNumber.
		// Pass through directly so the caller receives a structured response.
		if m, ok := st.(map[string]interface{}); ok {
			if _, hasExecID := m["executionId"]; !hasExecID {
				m["executionId"] = key
			}
			return m, nil
		}
		return map[string]interface{}{
			"executionId": key,
			"state":       st,
		}, nil
	}
}

func makeConfigReloadHandler(load func(root string) (interface{}, error), workRoot string) Handler {
	return func(ctx context.Context, payload json.RawMessage) (interface{}, error) {
		if load == nil {
			return nil, fmt.Errorf("config.reload: config loader not configured")
		}
		_, err := load(workRoot)
		if err != nil {
			return nil, fmt.Errorf("config.reload: %w", err)
		}
		return map[string]interface{}{
			"reloaded": true,
		}, nil
	}
}
