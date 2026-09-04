// Package execution manages pipeline execution — worktrees, skill process
// spawning, process lifecycle, and output streaming.
package execution

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
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
	return append(removeEnvVar(env, key), key+"="+value)
}

// removeEnvVar drops every entry for key from env. Used where the correct
// export is NO export: a variable whose readers test presence cannot be
// neutralized by setting it empty, and an inherited value is not a default —
// see composeStageEnv's run-identity reconcile.
func removeEnvVar(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
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

	// stopRequested is set the instant CancelWithGrace/StopExecution decide to
	// stop this execution — before SIGTERM is even sent — so RunStage can
	// later tell "the CLI trapped our SIGTERM and exited 0" apart from an
	// ordinary healthy exit (#564). It must be set before signalling, not
	// after the process exits: cmd.Wait() in RunStage races the goroutine
	// that observes the SIGTERM'd process exit, so a flag set only after that
	// race would sometimes lose it. atomic.Bool because CancelWithGrace reads
	// the map under m.mu but touches the Execution after releasing it.
	stopRequested atomic.Bool

	// done is closed by RunStage the instant its OWN cmd.Wait() returns (#564).
	// CancelWithGrace/StopExecution select on it instead of calling
	// Process.Wait() themselves.
	//
	// The reason: os.Process.Wait() calls syscall.Wait4 directly with no
	// dedup across callers (go.dev/issue/67642) — two concurrent Wait()s on
	// the SAME *os.Process both race the kernel reaper, and whichever loses
	// gets ECHILD ("wait: no child processes"), not the real exit status.
	// RunStage always calls cmd.Wait() (== Process.Wait() on this same
	// pointer) once the process exits; a second concurrent Wait() from
	// CancelWithGrace's own goroutine — the pre-#564 design — would win that
	// race often enough to matter (~40% locally): cmd.Wait() in RunStage then
	// returns the syscall error instead of a clean ExitCode 0, and this
	// feature's whole "ExitCode==0, Cancelled==true" shape never gets a
	// chance to form. done lets CancelWithGrace learn "the process exited"
	// from the ONE caller that actually reaps it, instead of reaping a
	// second time.
	//
	// nil for an Execution built outside RunStage (legacy direct-construction
	// tests, and any future caller with no reaper of its own): CancelWithGrace
	// falls back to reaping itself, unchanged from before this fix.
	done chan struct{}
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
// consistent with the run's on-disk state (trace, runtime-{issue}-{runId}.json). A nil
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

	// Stamp the worktree on the runtime the moment it exists (#399), not at
	// process registration below. Everything between here and cmd.Start() can
	// fail — model validation, the three pipes, the spawn itself — and each of
	// those exits used to leave WorktreeDir empty on a run whose worktree is
	// already on disk, so stageWorkspace fell back to the workspace root and
	// the failure path inspected the wrong tree. SetWorktree writes that one
	// field and deliberately not PID: no child exists yet.
	//
	// The stamp sits ABOVE the error check on purpose: ensureWorktree's own
	// provisioning continues after `git worktree add` (the SDK-CLI build), so
	// it can fail with the worktree already created. Its error contract is
	// "path non-empty iff the worktree exists on disk", which makes the path —
	// not the error — the authority on whether there is a tree to name.
	if worktreeDir != "" && opts.Runtime != nil {
		opts.Runtime.SetWorktree(worktreeDir)
	}
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
	runOpts := buildRunOptions(opts, worktreeDir)

	// Model↔provider validation (#4021): adapters exposing the optional
	// ValidateModel hook (Codex, Gemini) fail fast on an invalid model BEFORE
	// the command is built and the CLI is spawned. Adapters without the hook
	// are unaffected.
	if validator, ok := adapter.(interface{ ValidateModel(string) error }); ok {
		if err := validator.ValidateModel(runOpts.Model); err != nil {
			return nil, fmt.Errorf("model validation failed for adapter %q: %w", adapter.Name(), err)
		}
	}
	// Effort preflight (#569 → #606): adapters whose dispatch consumes the
	// envelope's effort half expose the optional ValidateEffort hook and gate
	// the value that will ACTUALLY dispatch (their own env-override-else-
	// RunOptions resolution) — fail fast BEFORE spawn, like the model hook.
	if validator, ok := adapter.(interface {
		ValidateEffort(model, effort string) error
	}); ok {
		if err := validator.ValidateEffort(runOpts.Model, runOpts.Effort); err != nil {
			return nil, fmt.Errorf("effort validation failed for adapter %q: %w", adapter.Name(), err)
		}
	}

	cmdName, args, env := adapter.BuildCommand(runOpts)

	// Prepare OS command
	cmd := exec.CommandContext(execCtx, cmdName, args...)
	cmd.Dir = worktreeDir

	// Spawn the stage as its own PROCESS-GROUP LEADER (#1253) so every kill
	// path can reach its descendants.
	//
	// Without this the stage shares the daemon's process group and there is no
	// group to signal, so `Process.Signal`/`Process.Kill` reach exactly one
	// pid. A stage that boots an emulator, a dev server or a database then
	// leaves them running when it is cancelled: on SIGTERM a shell child MAY
	// propagate through its own trap, but on SIGKILL — the path
	// CancelWithGrace takes once the grace period expires — no trap runs at
	// all, so the harder the kill the more certain the leak. The orphans are
	// reparented to PID 1 with nothing tying them back to the run, which is
	// the shape AGENTS.md describes for the spin loops that ran for eleven
	// hours at load average 253.
	//
	// Setpgid also detaches the stage from the daemon's controlling terminal
	// group, which is what we want for a headless child: an operator's Ctrl-C
	// reaches the daemon, and the daemon decides how to tear the stage down.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Merge environment
	cmd.Env = composeStageEnv(os.Environ(), env, opts.SkillPath, runOpts.RunID)

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
		// done is set HERE, not left nil, so CancelWithGrace/StopExecution's
		// waitForExit defers to this function's own cmd.Wait() below instead
		// of reaping the process a second time (#564 — see waitForExit).
		done: make(chan struct{}),
	}

	m.mu.Lock()
	m.running[execKey] = execution
	m.mu.Unlock()

	if opts.Runtime != nil {
		opts.Runtime.SetProcess(cmd.Process.Pid, worktreeDir)
		// PUBLISH THE LIVE CHILD (#555). SetProcess only writes memory, and the
		// scheduler then blocks in cmd.Wait() below until the stage exits — so
		// before this line the manager never regained control to persist, and the
		// only pid the run's snapshot ever carried on this path was the ZERO the
		// stage-start persist wrote (#534). The reconciler's liveness ladder reads
		// that file: arm 3 (processAlive(snap.PID)) was structurally false for
		// every scheduler-owned run, and once a single stage ran quietly past
		// runstate.LivenessWindow arm 4 went false too, so a HEALTHY run was
		// emitted as pipeline_done(success=false) and its snapshot deleted
		// mid-flight.
		//
		// One write, at the one instant the fact becomes true, is the whole fix:
		// a live pid is an OS-verified statement about the process actually doing
		// the run's work, and it stays true for exactly as long as the work does.
		// It is deliberately NOT a periodic heartbeat — a timer that keeps
		// stamping an mtime asserts "someone wrote recently", which is the same
		// class of evidence arm 4 already is, and it fails in both directions
		// (a wedged run whose ticker survives becomes immortal; a starved ticker
		// reaps a healthy run).
		m.publishStageChild(opts.Repo, opts.Runtime)
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
					"[model-refusal-fallback] %s#%d %s: claude CLI swapped %s → %s (category %q) after a safety refusal; attributing the served model — see docs/FAILURE_TAXONOMY.md § Model Refusal Fallback (#91)\n",
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
	// Signal waitForExit callers (CancelWithGrace/StopExecution) that the
	// process is reaped, THIS function is the one that reaped it — closing
	// immediately after cmd.Wait() returns, not after the map deletion below,
	// keeps the window a concurrent CancelWithGrace could still be blocked in
	// its own Process.Wait() as short as possible.
	close(execution.done)

	// Unregister execution
	m.mu.Lock()
	delete(m.running, execKey)
	m.mu.Unlock()

	// RETRACT THE CHILD (#555), the symmetric half of the publish above and the
	// reason the fix cannot create a false negative.
	//
	// The child has exited. Leaving its pid on disk would hand arm 3 a dead pid
	// that the kernel is free to recycle into an unrelated process, which is the
	// PID-reuse window #534 exists to bound — and here it would span the whole
	// between-stages gap (gates, git, CI waits) instead of one stage. Zero is
	// what is true now, `runstate.ProcessAlive` refuses it before it makes a
	// syscall, and the run is carried through the gap by arm 4's timestamp lease
	// exactly as it was before this change.
	//
	// This runs on EVERY exit path after Wait — clean exit, non-zero exit, and
	// the wait-error return below — because the retraction is about the child
	// being gone, not about how it went. SetStageChild, not SetProcess: the
	// worktree must survive for the failure path that inspects it (#399).
	if opts.Runtime != nil {
		opts.Runtime.SetStageChild(0)
		m.publishStageChild(opts.Repo, opts.Runtime)
	}

	result := runResultFromAccumulator(string(stdoutBuf), string(stderrBuf), tokenAcc, modelTracker)
	// #564: a graceful-stop CLI that traps SIGTERM and exits 0 is otherwise
	// indistinguishable from a healthy stage — ExitCode is 0 and cmd.Wait()
	// returns a nil error either way. execution.stopRequested is the ONLY
	// place that predicate is evaluated (per the issue's single-predicate
	// constraint); every other consumer reads RunResult.Cancelled instead of
	// re-deriving it from ctx.Err() or exit code.
	result.Cancelled = execution.stopRequested.Load()
	// The scheduler's legacy runner projection intentionally remains untouched:
	// stage-keyed runtime handoff lets its existing CompleteStage call consume
	// the cache pools without widening or editing scheduler.go.
	recordRunResultTokenCounts(opts.Runtime, opts.Stage, result)
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

// stageStateDir is the directory the run's runtime-{issue}-{runId}.json lives
// in, derived the same way every other writer derives it: the RUN'S TARGET REPO
// (#229/#215), never the launch root. RepoRoot and the scheduler's runRoot
// resolve through the same injected resolver — SetRepoPathResolver installs one
// into both — so the manager writes the file the scheduler already created
// rather than a second copy in another repo.
func (m *Manager) stageStateDir(repo string) string {
	return filepath.Join(m.RepoRoot(repo), ".nightgauge", "pipeline")
}

// publishStageChild flushes the runtime's CURRENT stage-child fact to the run's
// snapshot so the orphan reconciler's liveness ladder can read it (#555).
//
// PersistExisting, never Persist: the manager is not the snapshot's creator and
// must not become one. Creating a file here would manufacture a reconcilable
// orphan for a run whose owner never persisted anything (a direct RunStage
// caller), and — worse — could re-create a snapshot that a terminal claim had
// already sealed and removed between the spawn and this write, which is
// precisely the resurrection ADR-017 Decision 5 introduced PersistExisting to
// prevent.
//
// Best-effort, exactly like the scheduler's own persists. Three outcomes are
// NORMAL rather than faults and stay silent, or every identity-less/IPC-mode
// dispatch would log twice per stage:
//
//   - no file (os.ErrNotExist): this run has no snapshot to update. Nothing can
//     reap what does not exist, so there is nothing to fix.
//   - sealed (state.ErrRunSealed): the terminal claim already won; the run is
//     over and its file is deliberately gone.
//   - no identity (state.ErrNoRunIdentity): a runtime that cannot name a run has
//     no snapshot filename either.
func (m *Manager) publishStageChild(repo string, runtime *state.RuntimeState) {
	if runtime == nil {
		return
	}
	err := runtime.PersistExisting(m.stageStateDir(repo))
	switch {
	case err == nil,
		errors.Is(err, os.ErrNotExist),
		errors.Is(err, state.ErrRunSealed),
		errors.Is(err, state.ErrNoRunIdentity):
		return
	}
	// Anything else is a real write failure. It is not fatal — the stage runs
	// regardless — but it silently re-opens the false-reap window, so it gets a
	// line that names the consequence rather than just the errno.
	log.Printf("execution: could not publish the stage-child pid for %s#%d (run %s): %v — "+
		"the orphan reconciler's liveness ladder will fall back to the timestamp lease for this stage",
		repo, runtime.IssueNumber, runtime.RunID, err)
}

func runResultFromAccumulator(stdout, stderr string, tokenAcc *TokenAccumulator, modelTracker *ServedModelTracker) *adapters.RunResult {
	cacheCreation5m, cacheCreation1h := tokenAcc.CacheCreationByTTL()
	return &adapters.RunResult{
		Stdout:                stdout,
		Stderr:                stderr,
		InputTokens:           tokenAcc.InputTokens,
		OutputTokens:          tokenAcc.OutputTokens,
		CacheReadTokens:       tokenAcc.CacheRead,
		CacheCreationTokens:   cacheCreation5m + cacheCreation1h,
		CacheCreation5mTokens: cacheCreation5m,
		CacheCreation1hTokens: cacheCreation1h,
		PremiumRequests:       tokenAcc.PremiumRequests,
		ServedModel:           modelTracker.ServedModel,
	}
}

func recordRunResultTokenCounts(runtime *state.RuntimeState, stage string, result *adapters.RunResult) {
	if runtime == nil || result == nil {
		return
	}
	runtime.RecordStageTokenCounts(state.PipelineStage(stage), tokens.TokenCounts{
		CacheRead:       result.CacheReadTokens,
		CacheCreation5m: result.CacheCreation5mTokens,
		CacheCreation1h: result.CacheCreation1hTokens,
	})
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

	execution.stopRequested.Store(true)

	// Send SIGTERM first for graceful shutdown
	if execution.Process != nil {
		signalProcessTree(execution.Process, syscall.SIGTERM)

		// Give 5 seconds for graceful shutdown
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()

		select {
		case <-waitForExit(execution):
			// Process exited gracefully
		case <-timer.C:
			// Force kill — the group, not the leader (#1253). SIGKILL runs no
			// trap, so anything the stage spawned outlives a per-pid kill.
			signalProcessTree(execution.Process, syscall.SIGKILL)
		}
	}

	execution.Cancel()
	return nil
}

// waitForExit reports when ex's process has exited, without itself reaping it
// when something else already will.
//
// ex.done, when set, is closed by RunStage's OWN cmd.Wait() (#564) — reuse
// that signal instead of calling ex.Process.Wait() a second time. Two
// concurrent Wait() calls on the same *os.Process both race the kernel
// reaper (os.Process.Wait calls syscall.Wait4 with no dedup across callers,
// go.dev/issue/67642): whichever loses gets ECHILD, not the real exit status.
// For an execution RunStage owns, that loser was reliably cmd.Wait() itself —
// observed locally as `wait: wait: no child processes` on ~40% of runs, which
// would have made this fix's own reported ExitCode/Cancelled shape as
// unreliable as the bug it exists to close.
//
// ex.done is nil for an Execution built outside RunStage (direct-construction
// tests, any future caller with no reaper of its own) — fall back to the
// pre-#564 self-reap so that shape is unaffected.
func waitForExit(ex *Execution) <-chan struct{} {
	if ex.done != nil {
		return ex.done
	}
	done := make(chan struct{})
	go func() {
		_, _ = ex.Process.Wait()
		close(done)
	}()
	return done
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

	ex.stopRequested.Store(true)

	graceful := false
	if ex.Process != nil {
		signalProcessTree(ex.Process, syscall.SIGTERM)

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-waitForExit(ex):
			graceful = true
		case <-timer.C:
			// The grace period expired. SIGKILL cannot be trapped, so a
			// per-pid kill here is precisely when descendants leak (#1253):
			// no shell trap will run to take them down. Kill the group.
			signalProcessTree(ex.Process, syscall.SIGKILL)
		}
	}

	ex.Cancel()
	return graceful, nil
}

// signalProcessTree delivers sig to the stage's whole PROCESS GROUP, falling
// back to the single process when the group cannot be resolved (#1253).
//
// Stages are spawned with Setpgid (see startProcess), which makes the child a
// group leader whose pgid equals its pid — so `kill(-pid, sig)` reaches the
// stage AND everything it spawned. Signalling the bare pid reached only the
// direct child, and every grandchild survived, reparented to PID 1.
//
// The fallback matters more than it looks. If a stage was started before this
// change, or Setpgid failed, or the child already exited and its group is
// gone, syscall.Kill(-pid, …) returns ESRCH — and a kill path that treated
// that as "done" would silently signal NOTHING. Falling back to the process
// keeps the old behaviour as the floor: this can reach more than before, never
// less.
//
// Returns whether anything was signalled, so a caller can tell "reaped" from
// "there was nothing to reap".
func signalProcessTree(proc *os.Process, sig syscall.Signal) bool {
	if proc == nil {
		return false
	}
	// Negative pid == "the process group led by pid". Only meaningful because
	// startProcess made the child a group leader.
	if err := syscall.Kill(-proc.Pid, sig); err == nil {
		return true
	}
	return proc.Signal(sig) == nil
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
		signalProcessTree(ex.Process, syscall.SIGTERM)
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
		signalProcessTree(ex.Process, syscall.SIGSTOP)
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
		signalProcessTree(ex.Process, syscall.SIGCONT)
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
	Repo        string
	IssueNumber int
	Stage       string
	SkillPath   string
	ContextFile string
	OutputFile  string
	Model       string
	// Effort is the dispatch envelope's effort half (#581/#606) — see
	// adapters.RunOptions.Effort for the consumption contract.
	Effort       string
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

// buildRunOptions maps the orchestrator-layer StageOptions onto the
// adapter-layer RunOptions for a stage about to be spawned in worktreeDir.
//
// This is the ONE place the two layers meet, extracted from RunStage so the
// mapping is assertable without spawning a process. Anything StageOptions
// carries and this function drops is, by construction, invisible to every
// adapter — which is how the run identity went missing before #370.
func buildRunOptions(opts StageOptions, worktreeDir string) adapters.RunOptions {
	// The run identity is read straight off the runtime StageOptions already
	// carries (ADR-017 step 0) — there is deliberately no parallel
	// StageOptions.RunID scalar to drift from it. A nil Runtime is a real
	// configuration, not an error: the autonomous issue-refine dispatch has no
	// run identity, and its stages must export no NIGHTGAUGE_RUN_ID at all.
	runID := ""
	if opts.Runtime != nil {
		runID = opts.Runtime.RunID
	}

	return adapters.RunOptions{
		SkillPath:    opts.SkillPath,
		WorktreeDir:  worktreeDir,
		ContextFile:  opts.ContextFile,
		OutputFile:   opts.OutputFile,
		IssueNumber:  opts.IssueNumber,
		Repo:         opts.Repo,
		Stage:        opts.Stage,
		Model:        opts.Model,
		Effort:       opts.Effort,
		MaxTokens:    opts.MaxTokens,
		AllowedTools: opts.AllowedTools,
		Prompt:       opts.Prompt,
		MaxTurns:     opts.MaxTurns,
		CostBudget:   opts.CostBudget,
		TargetRepo:   opts.TargetRepo,
		RunID:        runID,
	}
}

// composeStageEnv builds the environment a stage subprocess actually receives:
// the host environment, plus the adapter's own exports, plus the run-scoped
// exports the manager owns.
//
// Extracted from RunStage for the same reason buildRunOptions was. This env IS
// the interface between this process and the child; inline in a function whose
// next statement spawns a process, it was assertable only by spawning one.
func composeStageEnv(base []string, adapterEnv map[string]string, skillPath, runID string) []string {
	// Deterministic Node for the stage subprocess (#3863): a non-interactive
	// spawn does not inherit the login shell's nvm PATH, so resolve Node from
	// the host's nvm `default` alias and prepend it. No-op when node is already
	// on PATH (hosted runners) or unresolvable.
	env, _ := applyNodeResolution(base)
	for k, v := range adapterEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
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
		env = upsertEnvVar(env, "NIGHTGAUGE_BIN", self)
	}

	// Export the absolute skill directory so agents resolve _includes/_shared
	// supporting files without CWD assumptions or whole-filesystem scans in
	// cross-repo worktrees (#196 — agents previously ran `find / -maxdepth 6`
	// and read stale copies from ~/.codex/skills).
	if skillPath != "" {
		env = upsertEnvVar(env, "NIGHTGAUGE_SKILL_DIR", filepath.Dir(skillPath))
	}

	// Run identity, reconciled against the INHERITED environment rather than
	// merely added to it (ADR-017). nightgauge dispatches nightgauge: a stage
	// subprocess runs under NIGHTGAUGE_RUN_ID=A, and anything it launches — the
	// recursive-dogfood case, a manual per-stage invocation, the autonomous
	// issue-refine CLI dispatch — inherits A. If that inner dispatch has no run
	// identity of its own, leaving A in place books its records under a run it
	// has nothing to do with: identity laundering, the exact class this ADR
	// exists to close. So strip when this dispatch has no identity, and upsert
	// (never bare-append) when it does, so the value that survives is this
	// dispatch's and not the host's.
	if runID != "" {
		env = upsertEnvVar(env, adapters.RunIDEnvVar, runID)
	} else {
		env = removeEnvVar(env, adapters.RunIDEnvVar)
	}

	return env
}

// ExecutionInfo is a summary of a running execution (safe for serialization).
type ExecutionInfo struct {
	Repo        string        `json:"repo"`
	IssueNumber int           `json:"issueNumber"`
	Stage       string        `json:"stage"`
	PID         int           `json:"pid"`
	Duration    time.Duration `json:"duration"`
}
