// Package execution manages pipeline execution — worktrees, skill process
// spawning, process lifecycle, and output streaming.
package execution

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	"github.com/nightgauge/nightgauge/internal/state"
)

// hostBinaryPath returns the path of the running nightgauge binary so it
// can be exported to skill subprocesses as $NIGHTGAUGE_BIN, or "" when the
// executable path cannot be resolved. The skill PREFLIGHT cascade honors
// $NIGHTGAUGE_BIN first, making binary discovery provider-neutral without
// any VSCode-extension-specific path (Issue #4029). `executable` is injectable
// (os.Executable in production) so the resolve-failure path is testable.
// Best-effort by design: "" simply falls through to the skill's
// PATH/repo/canonical/go-bin fallbacks.
func hostBinaryPath(executable func() (string, error)) string {
	self, err := executable()
	if err != nil {
		return ""
	}
	return self
}

// upsertEnvVar sets key=value in a KEY=VALUE environment slice, replacing any
// existing entry for key so the new value is authoritative. A plain append
// would leave a duplicate key whose precedence is OS-dependent; upsert keeps the
// host-provided value unambiguous (#4029). Returns a new slice; the input is
// not mutated.
func upsertEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

// Manager orchestrates skill execution for pipeline stages.
type Manager struct {
	workspaceRoot string
	// repoPathResolver, when set, maps an "owner/repo" slug to that repo's
	// filesystem root so worktrees resolve into the run's target repo instead
	// of the single launch/workspace root (#229). nil (CLI/auto, single-repo)
	// falls back to workspaceRoot — additive, existing behavior unchanged.
	repoPathResolver func(repo string) string
	adapter          adapters.SkillRunner
	mu               sync.Mutex
	running          map[string]*Execution // keyed by "repo#issue"
}

// Execution represents a single running pipeline execution.
type Execution struct {
	Repo        string
	IssueNumber int
	Runtime     *state.RuntimeState
	Process     *os.Process
	Cancel      context.CancelFunc
	Streamer    adapters.OutputStreamer
}

// NewManager creates an execution manager.
func NewManager(workspaceRoot string, adapter adapters.SkillRunner) *Manager {
	return &Manager{
		workspaceRoot: workspaceRoot,
		adapter:       adapter,
		running:       make(map[string]*Execution),
	}
}

// WorkspaceRoot returns the workspace root directory.
func (m *Manager) WorkspaceRoot() string {
	return m.workspaceRoot
}

// SetRepoPathResolver installs a resolver mapping an "owner/repo" slug to that
// repo's filesystem root. In a multi-repo workspace the scheduler wires this
// from the IPC ClientResolver so worktrees land in the run's target repo, kept
// consistent with the run's on-disk state (trace, runtime-{N}.json). A nil
// resolver or an unregistered repo falls back to workspaceRoot, so single-repo
// / CLI / auto behavior is byte-identical (#229).
func (m *Manager) SetRepoPathResolver(fn func(repo string) string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repoPathResolver = fn
}

// RepoRoot resolves the filesystem root for the given "owner/repo" slug: the
// resolver's path when a resolver is set and yields a non-empty root, else the
// workspace root (the additive single-repo default). Mutex-guarded like
// AdapterName/HasAdapter.
func (m *Manager) RepoRoot(repo string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repoPathResolver != nil {
		if root := m.repoPathResolver(repo); root != "" {
			return root
		}
	}
	return m.workspaceRoot
}

// SetAdapter changes the active skill runner adapter.
func (m *Manager) SetAdapter(adapter adapters.SkillRunner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adapter = adapter
}

// AdapterName returns the active adapter's name, or "" when none is
// configured (IPC mode). Used by the scheduler's per-stage adapter
// resolution to avoid redundant SetAdapter churn (#54).
func (m *Manager) AdapterName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adapter == nil {
		return ""
	}
	return m.adapter.Name()
}

// HasAdapter reports whether a skill runner adapter is configured. Callers
// that invoke RunStage directly (e.g. autonomous refinement) should check
// this first: in VSCode IPC mode the adapter is intentionally nil and
// execution must be routed through the IPC stage runner instead.
func (m *Manager) HasAdapter() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.adapter != nil
}

// RunStage executes a single pipeline stage for an issue.
func (m *Manager) RunStage(ctx context.Context, opts StageOptions) (*adapters.RunResult, error) {
	m.mu.Lock()
	adapter := m.adapter
	m.mu.Unlock()
	if adapter == nil {
		return nil, fmt.Errorf("execution manager has no skill runner adapter configured — RunStage requires a CLI adapter (IPC mode must use IpcStageRunner instead)")
	}

	// Agentic truth-gate (#57): pipeline stages require a real tool loop.
	// Chat-completion-only adapters would emit prose instead of commits —
	// reject BEFORE any spawn, with remediation. Eval/judge surfaces do not
	// dispatch through RunStage and keep chat-only adapters.
	if !adapter.Agentic() {
		return nil, fmt.Errorf(
			"adapter %q is chat-completion-only (no agentic tool loop): pipeline stages cannot edit files, run shell commands, or call gh through it; set an agentic adapter (claude, claude-sdk, codex, gemini, gemini-sdk, copilot) via --adapter or NIGHTGAUGE_ADAPTER",
			adapter.Name(),
		)
	}

	execCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Create or reuse worktree
	worktreeDir, err := m.ensureWorktree(opts.Repo, opts.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("worktree setup: %w", err)
	}

	// Provision Codex provider context on the Go-direct spawn path (#4041):
	// AGENTS.md baseline steering (#4028) and $CODEX_HOME/config.toml MCP servers
	// (#4025), at parity with the TypeScript StageExecutor. No-op for non-codex
	// adapters. Best-effort — a provisioning failure is logged but never blocks
	// the stage (mirrors the TS `.catch(() => {})`), since the CLI can still run
	// without the extra steering/MCP wiring.
	if res, perr := codexprovision.Provision(adapter.Name(), worktreeDir); perr != nil {
		fmt.Fprintf(os.Stderr, "[codex-provision] non-fatal: %v\n", perr)
	} else if len(res.SkippedCollisions) > 0 {
		fmt.Fprintf(os.Stderr, "[codex-provision] skipped user-defined MCP servers: %s\n", strings.Join(res.SkippedCollisions, ", "))
	}

	// Build command from adapter
	runOpts := adapters.RunOptions{
		SkillPath:    opts.SkillPath,
		WorktreeDir:  worktreeDir,
		ContextFile:  opts.ContextFile,
		OutputFile:   opts.OutputFile,
		IssueNumber:  opts.IssueNumber,
		Repo:         opts.Repo,
		Stage:        opts.Stage,
		Model:        opts.Model,
		MaxTokens:    opts.MaxTokens,
		AllowedTools: opts.AllowedTools,
		Prompt:       opts.Prompt,
		MaxTurns:     opts.MaxTurns,
		CostBudget:   opts.CostBudget,
		TargetRepo:   opts.TargetRepo,
	}

	// Model↔provider validation (#4021): adapters exposing the optional
	// ValidateModel hook (Codex, Gemini) fail fast on an invalid model BEFORE
	// the command is built and the CLI is spawned. Adapters without the hook
	// are unaffected.
	if validator, ok := adapter.(interface{ ValidateModel(string) error }); ok {
		if err := validator.ValidateModel(runOpts.Model); err != nil {
			return nil, fmt.Errorf("model validation failed for adapter %q: %w", adapter.Name(), err)
		}
	}

	cmdName, args, env := adapter.BuildCommand(runOpts)

	// Prepare OS command
	cmd := exec.CommandContext(execCtx, cmdName, args...)
	cmd.Dir = worktreeDir

	// Merge environment
	cmd.Env = os.Environ()
	// Deterministic Node for the stage subprocess (#3863): a non-interactive
	// spawn does not inherit the login shell's nvm PATH, so resolve Node from
	// the host's nvm `default` alias and prepend it. No-op when node is already
	// on PATH (hosted runners) or unresolvable.
	cmd.Env, _ = applyNodeResolution(cmd.Env)
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	// Export the running binary so skill subprocesses discover it under any
	// adapter via $NIGHTGAUGE_BIN — the skill's PREFLIGHT cascade honors
	// this first, then prepends its dir to PATH for bare `nightgauge …`
	// calls. This removes the need for any VSCode-extension-specific binary
	// path in skills (Issue #4029). Upserted (not appended) so the host value is
	// authoritative — no duplicate NIGHTGAUGE_BIN with OS-dependent
	// precedence if one was inherited. Best-effort: a failure to resolve self
	// never blocks the spawn (the cascade has PATH/repo fallbacks).
	if self := hostBinaryPath(os.Executable); self != "" {
		cmd.Env = upsertEnvVar(cmd.Env, "NIGHTGAUGE_BIN", self)
	}

	// Export the absolute skill directory so agents resolve _includes/_shared
	// supporting files without CWD assumptions or whole-filesystem scans in
	// cross-repo worktrees (#196 — agents previously ran `find / -maxdepth 6`
	// and read stale copies from ~/.codex/skills).
	if opts.SkillPath != "" {
		cmd.Env = upsertEnvVar(cmd.Env, "NIGHTGAUGE_SKILL_DIR", filepath.Dir(opts.SkillPath))
	}

	// Set up stdin pipe for adapters that receive prompt via stdin
	var stdinPipe io.WriteCloser
	if adapter.UsesStdin() && opts.Prompt != "" {
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin pipe: %w", err)
		}
	}

	// Set up output streaming
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	// Start process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmdName, err)
	}

	// Write prompt to stdin and close (signals EOF to start processing)
	if stdinPipe != nil {
		go func() {
			_, _ = io.WriteString(stdinPipe, opts.Prompt)
			_ = stdinPipe.Close()
		}()
	}

	// Register execution
	execKey := fmt.Sprintf("%s#%d", opts.Repo, opts.IssueNumber)
	execution := &Execution{
		Repo:        opts.Repo,
		IssueNumber: opts.IssueNumber,
		Runtime:     opts.Runtime,
		Process:     cmd.Process,
		Cancel:      cancel,
		Streamer:    opts.Streamer,
	}

	m.mu.Lock()
	m.running[execKey] = execution
	m.mu.Unlock()

	if opts.Runtime != nil {
		opts.Runtime.SetProcess(cmd.Process.Pid, worktreeDir)
	}

	// Stream output concurrently, parsing NDJSON for token counts
	var wg sync.WaitGroup
	var stdoutBuf, stderrBuf []byte
	tokenAcc := &TokenAccumulator{}
	// Served-model attribution (#91): the claude CLI can silently swap to a
	// fallback model on a safety refusal (model_refusal_fallback) and still
	// exit 0. Track what the stream actually reports so cost/telemetry
	// attribute the serving model, not the requested one. Only touched by the
	// stdout goroutine and read after wg.Wait(), like stdoutBuf.
	modelTracker := &ServedModelTracker{}
	streamFmt := StreamFormatForAdapter(adapter.Name())

	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		// Deterministic phase inference (Issue #3760): some stages (notably the
		// edit-heavy feature-dev) don't reliably emit phase markers, so infer
		// progress from observed tool activity. No-op for self-reporting stages;
		// monotonic; real markers take precedence via ObserveRealMarker.
		inferer := NewPhaseInferer(opts.Stage)
		started := false
		for scanner.Scan() {
			line := scanner.Bytes()
			stdoutBuf = append(stdoutBuf, line...)
			stdoutBuf = append(stdoutBuf, '\n')
			lineStr := string(line)
			// Parse NDJSON for token usage (adapter-specific format)
			event, _ := tokenAcc.ParseLine(streamFmt, lineStr)
			// Track the serving model; a refusal fallback gets one observable
			// log line the moment it fires (#91).
			if fb := modelTracker.Observe(event); fb != nil {
				fmt.Fprintf(os.Stderr,
					"[model-refusal-fallback] %s#%d %s: claude CLI swapped %s → %s (category %q) after a safety refusal; attributing the served model — see docs/spikes/fable-5-behavior-porting.md §8.3 (#91)\n",
					opts.Repo, opts.IssueNumber, opts.Stage,
					fb.OriginalModel, fb.FallbackModel, fb.RefusalCategory)
			}
			// Detect phase markers in skill output
			if opts.PhaseEventFn != nil {
				// Emit the stage's first phase as soon as output starts so
				// non-self-reporting stages show a live phase immediately.
				if !started {
					started = true
					if m, ok := inferer.Start(); ok {
						opts.PhaseEventFn(m.Stage, m.Name, m.Index, m.Total)
					}
				}
				if marker, ok := ParsePhaseMarker(lineStr); ok {
					inferer.ObserveRealMarker(marker.Index) // real marker wins
					opts.PhaseEventFn(marker.Stage, marker.Name, marker.Index, marker.Total)
				}
				// Infer phase advancement from assistant-message tool calls.
				for _, tu := range extractToolUses(lineStr) {
					if m, ok := inferer.ObserveToolUse(tu.Name, tu.Input); ok {
						opts.PhaseEventFn(m.Stage, m.Name, m.Index, m.Total)
					}
				}
			}
			if opts.Streamer != nil {
				opts.Streamer.OnOutput("stdout", append(line, '\n'))
			}
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			stderrBuf = append(stderrBuf, line...)
			stderrBuf = append(stderrBuf, '\n')
			if opts.Streamer != nil {
				opts.Streamer.OnOutput("stderr", append(line, '\n'))
			}
		}
	}()

	// Wait for output to drain, then wait for process
	wg.Wait()
	err = cmd.Wait()

	// Unregister execution
	m.mu.Lock()
	delete(m.running, execKey)
	m.mu.Unlock()

	result := &adapters.RunResult{
		Stdout:          string(stdoutBuf),
		Stderr:          string(stderrBuf),
		InputTokens:     tokenAcc.InputTokens,
		OutputTokens:    tokenAcc.OutputTokens,
		PremiumRequests: tokenAcc.PremiumRequests,
		ServedModel:     modelTracker.ServedModel,
	}
	if fb := modelTracker.Fallback; fb != nil {
		result.RefusalFallbackFrom = fb.OriginalModel
		result.RefusalFallbackTo = fb.FallbackModel
		result.RefusalFallbackCategory = fb.RefusalCategory
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("wait: %w", err)
		}
	}

	if opts.Streamer != nil {
		opts.Streamer.OnComplete(*result)
	}

	return result, nil
}

// StopExecution gracefully stops a running execution.
func (m *Manager) StopExecution(repo string, issueNumber int) error {
	execKey := fmt.Sprintf("%s#%d", repo, issueNumber)

	m.mu.Lock()
	execution, ok := m.running[execKey]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("no running execution for %s", execKey)
	}

	// Send SIGTERM first for graceful shutdown
	if execution.Process != nil {
		_ = execution.Process.Signal(syscall.SIGTERM)

		// Give 5 seconds for graceful shutdown
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()

		done := make(chan struct{})
		go func() {
			_, _ = execution.Process.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Process exited gracefully
		case <-timer.C:
			// Force kill
			_ = execution.Process.Kill()
		}
	}

	execution.Cancel()
	return nil
}

// ListRunning returns all currently running executions.
func (m *Manager) ListRunning() []ExecutionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []ExecutionInfo
	for _, exec := range m.running {
		info := ExecutionInfo{
			Repo:        exec.Repo,
			IssueNumber: exec.IssueNumber,
		}
		if exec.Runtime != nil {
			info.Stage = string(exec.Runtime.Stage)
			info.Duration = exec.Runtime.TotalDuration()
		}
		if exec.Process != nil {
			info.PID = exec.Process.Pid
		}
		infos = append(infos, info)
	}
	return infos
}

// CancelWithGrace gracefully stops a running execution.
// It sends SIGTERM and waits up to timeout for the process to exit.
// If the process is still running after timeout, it sends SIGKILL (force kill).
// It always calls the execution's context cancel function.
// Returns true if the process exited within the grace period, false if force-killed
// or if no execution was found for key.
func (m *Manager) CancelWithGrace(key string, timeout time.Duration) (bool, error) {
	m.mu.Lock()
	ex, ok := m.running[key]
	m.mu.Unlock()
	if !ok {
		return false, nil
	}

	graceful := false
	if ex.Process != nil {
		_ = ex.Process.Signal(syscall.SIGTERM)

		done := make(chan struct{})
		go func() {
			_, _ = ex.Process.Wait()
			close(done)
		}()

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-done:
			graceful = true
		case <-timer.C:
			_ = ex.Process.Kill()
		}
	}

	ex.Cancel()
	return graceful, nil
}

// Stop stops a running execution by key (format: "owner/repo#number").
func (m *Manager) Stop(key string) {
	m.mu.Lock()
	ex, ok := m.running[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	if ex.Process != nil {
		_ = ex.Process.Signal(syscall.SIGTERM)
	}
	ex.Cancel()
}

// Pause sends SIGSTOP to a running execution.
func (m *Manager) Pause(key string) {
	m.mu.Lock()
	ex, ok := m.running[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	if ex.Process != nil {
		_ = ex.Process.Signal(syscall.SIGSTOP)
	}
}

// Resume sends SIGCONT to a paused execution.
func (m *Manager) Resume(key string) {
	m.mu.Lock()
	ex, ok := m.running[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	if ex.Process != nil {
		_ = ex.Process.Signal(syscall.SIGCONT)
	}
}

// GetState returns runtime state for an execution, or nil if not found.
func (m *Manager) GetState(key string) interface{} {
	m.mu.Lock()
	ex, ok := m.running[key]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	if ex.Runtime == nil {
		return nil
	}
	return ex.Runtime.Snapshot()
}

// StageOptions holds all parameters for running a pipeline stage.
type StageOptions struct {
	Repo         string
	IssueNumber  int
	Stage        string
	SkillPath    string
	ContextFile  string
	OutputFile   string
	Model        string
	MaxTokens    int
	Timeout      time.Duration
	Runtime      *state.RuntimeState
	Streamer     adapters.OutputStreamer
	AllowedTools []string // Tools allowed for this skill (from SKILL.md frontmatter)
	Prompt       string   // Built prompt to pass via stdin (for Claude adapter)
	MaxTurns     int      // Max conversation turns
	CostBudget   float64  // Max cost in USD
	TargetRepo   string   // Expected repo for skill verification (owner/repo)

	// PhaseEventFn is called when a phase:start marker is detected in skill stdout.
	// Arguments: stage name, phase name, index, total.
	PhaseEventFn func(stage, name string, index, total int)
}

// ExecutionInfo is a summary of a running execution (safe for serialization).
type ExecutionInfo struct {
	Repo        string        `json:"repo"`
	IssueNumber int           `json:"issueNumber"`
	Stage       string        `json:"stage"`
	PID         int           `json:"pid"`
	Duration    time.Duration `json:"duration"`
}
