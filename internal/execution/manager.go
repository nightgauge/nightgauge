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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/codexprovision"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
// hostExecutable resolves THIS process's own binary for the NIGHTGAUGE_BIN
// export below. It is a package variable rather than a direct os.Executable
// reference because NIGHTGAUGE_BIN is not merely informational any more: the
// OpenCode plugin SPAWNS it, as `nightgauge hook stop-verify` on every
// session.idle (#1641). Under `go test`, os.Executable is the Go TEST
// binary, so that spawn re-runs the whole test binary — measured on
// internal/execution's own suite at 104.5 s of forked git work, orphaned
// past the test that started it, and bounded only by the plugin's 5 s kill.
// That, not any production cost, is what pushed
// TestOpenCodeIntegrationInheritUserConfigOptIn from ~8-12 s towards its
// 20 s bound (#1810). An integration test that dispatches the real OpenCode
// CLI points this at a real nightgauge build instead
// (useRealNightgaugeBinary).
var hostExecutable = os.Executable

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
	// The other half of the steering lifecycle (issue 1675). The managed block is
	// ephemeral, and this path used to write it and never remove it, so every
	// later commit in the worktree published it. The deferred call runs on
	// every exit from here — clean exit, failure, and the early returns before
	// spawn — removes the block from the working tree, and repairs HEAD when
	// the stage's own agent committed it while it was present. It runs for
	// every adapter: any stage can commit a block an interrupted Codex stage
	// left behind. context.Background: the stage context may already be done.
	defer func() {
		rep, aerr := codexprovision.AfterStage(context.Background(), adapter.Name(), worktreeDir)
		switch {
		case aerr != nil:
			fmt.Fprintf(os.Stderr, "[codex-provision] steering cleanup failed: %v\n", aerr)
		case rep.Repaired:
			fmt.Fprintf(os.Stderr, "[codex-provision] removed generated steering the stage committed (%s -> %s)\n", codexprovision.ShortSHA(rep.OldHead), codexprovision.ShortSHA(rep.NewHead))
			if rep.PushErr != nil {
				fmt.Fprintf(os.Stderr, "[codex-provision] repair not pushed; the next pipeline push carries it: %v\n", rep.PushErr)
			}
		case rep.Pushed:
			fmt.Fprintf(os.Stderr, "[codex-provision] published an earlier steering repair to the branch upstream\n")
		}
	}()

	// Build command from adapter
	runOpts := buildRunOptions(opts, worktreeDir)

	// Pre-dispatch gate (ADR-022): an adapter that can refuse a dispatch for a
	// reason other than its model or effort exposes the optional PreDispatch
	// hook — the opencode adapter's experimental enable gate is the first. It
	// runs after worktree setup, so a check that has to inspect the tree the
	// stage will run in can join it, and ahead of the model and effort checks
	// and BuildCommand, so a refusal states the real reason and spawns nothing.
	//
	// A stage whose context is done by now is not dispatched: the hook is not
	// called, so none of its probes starts, and nothing below runs. The hook
	// gets the stage's context, so a probe it starts is killed when the stage
	// is (#1627).
	if err := execCtx.Err(); err != nil {
		return nil, fmt.Errorf("stage not dispatched: %w", err)
	}
	if gate, ok := adapter.(interface {
		PreDispatch(context.Context, adapters.RunOptions) error
	}); ok {
		if err := gate.PreDispatch(execCtx, runOpts); err != nil {
			return nil, fmt.Errorf("dispatch refused for adapter %q: %w", adapter.Name(), err)
		}
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

	// Per-run root (ADR-022 § 8, § 22): an adapter whose CLI keeps state of
	// its own exposes the optional PrepareRunRoot hook and gets a root private
	// to the run, shared by every stage of it. Only the opencode adapter has
	// one. It is prepared after every check above, so a refused dispatch
	// creates nothing, and before BuildCommand, which points the CLI at it.
	//
	// The root is named by the run identity, and the run's end deletes it
	// (Scheduler.runPipeline's terminal defer, CleanupOpenCodeRunRoot). A
	// dispatch with no identity gets an id minted for it alone, never exported
	// as NIGHTGAUGE_RUN_ID, and its root is deleted when this call returns.
	if preparer, ok := adapter.(interface {
		PrepareRunRoot(adapters.RunRootRequest) (*adapters.RunRoot, error)
	}); ok {
		id := runOpts.RunID
		if !runstate.IsIdentity(id) {
			minted, mintErr := runstate.NewRunID()
			if mintErr != nil {
				return nil, fmt.Errorf("per-run root for adapter %q: mint an id: %w", adapter.Name(), mintErr)
			}
			id = minted
			defer func() {
				if err := m.CleanupOpenCodeRunRoot(id); err != nil {
					fmt.Fprintf(os.Stderr, "[opencode] could not delete the per-run root of a dispatch with no run identity: %v\n", err)
				}
			}()
		}
		machineDir, dirErr := config.MachineConfigDir()
		if dirErr != nil {
			return nil, fmt.Errorf("per-run root for adapter %q: resolve the machine-tier config directory: %w", adapter.Name(), dirErr)
		}
		root, prepErr := preparer.PrepareRunRoot(adapters.RunRootRequest{ID: id, MachineConfigDir: machineDir, Run: runOpts, WorkspaceRoot: m.workspaceRoot, Context: ctx})
		if prepErr != nil {
			return nil, fmt.Errorf("dispatch refused for adapter %q: %w", adapter.Name(), prepErr)
		}
		runOpts.RunRoot = root
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

	// Merge environment. An adapter whose CLI must not inherit some host
	// variables decides which through the optional WithholdsEnv hook, found
	// the same way as the hooks above, one variable name at a time (ADR-022
	// § 8: OpenCode inherits no OPENCODE_* variable and no other model
	// service's API key).
	var withhold func(key string) bool
	if w, ok := adapter.(interface {
		WithholdsEnv(adapters.RunOptions, string) bool
	}); ok {
		withhold = func(key string) bool { return w.WithholdsEnv(runOpts, key) }
	}
	cmd.Env = composeStageEnv(os.Environ(), withhold, env, opts.SkillPath, runOpts.RunID)

	// Output redaction (ADR-022 § 22): an adapter that hands its child secrets
	// through the environment names those variables through the optional
	// RedactedEnv hook, and their values are removed from every line the child
	// prints before it is streamed or kept.
	var redact *strings.Replacer
	if r, ok := adapter.(interface {
		RedactedEnv(adapters.RunOptions) []string
	}); ok {
		redact = envValueRedactor(cmd.Env, r.RedactedEnv(runOpts))
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

	// OpenCode (ADR-022): its own parser, whose run state also watches
	// stderr, before redaction, for permissions OpenCode rejected on its own,
	// and keeps no rejected call's input and no line after the first such
	// notice but the parser's own; every line is also redacted of credential
	// shapes, not only of the variables above, and a JSON event's strings
	// are redacted decoded as well as escaped; and a line over the scanner's
	// limit is dropped with a drift marker instead of ending the read, its
	// ends still read for a notice.
	//
	// Its cost watchdog (ADR-022 § 3) re-prices the stage from the model
	// registry on every step_finish and stops the stage once that passes
	// RunOptions.CostBudget, since OpenCode takes no cost cap of its own; the
	// subagent sessions the stream never carries are priced in once the stage
	// has ended. The model is the value BuildCommand passed as -m: the model
	// check before dispatch refused any model it would not pass.
	redactOut := func(b []byte) []byte { return redactLine(redact, b) }
	var openCode *openCodeRun
	var openCodeModel string
	var costCap *openCodeCostWatchdog
	if streamFmt == StreamFormatOpenCode {
		openCode = newOpenCodeRun(tokenAcc.OpenCode(), runOpts.AllowedTools)
		redactOut = openCodeOutputRedactor(redact)
		openCodeModel, _ = adapters.OpenCodeModelArg(runOpts.Model)
		costCap = newOpenCodeCostWatchdog(openCodeModel, runOpts.CostBudget, os.Stderr,
			fmt.Sprintf("%s#%d %s", opts.Repo, opts.IssueNumber, opts.Stage))
	}
	// Nightgauge OpenCode plugin handshake (#1635, ADR-022): the manager is
	// the one reader of both the nonce and the sentinel path opencode.go's
	// InstallNightgaugePlugin minted into the run's own environment, so it
	// verifies without a second implementation of either. handshakeChecked
	// and firstToolUseAt are written only by the stdout goroutine below and
	// read only after wg.Wait() (or, for the immediate kill, from inside that
	// same goroutine) — the same single-writer-then-barrier discipline
	// costCap.fired already relies on.
	var pluginHandshake opencodeplugin.HandshakeConfig
	var pluginHandshakeOK bool
	var handshakeChecked bool
	var firstToolUseAt time.Time
	var handshakeFailure string
	var operatorInstallRisk string
	if openCode != nil && runOpts.RunRoot != nil {
		pluginHandshake, pluginHandshakeOK = opencodeplugin.HandshakeConfigFromEnv(runOpts.RunRoot.Env)
		operatorInstallRisk = runOpts.RunRoot.Env[opencodeplugin.EnvOperatorInstallRisk]
	}
	// Operator install risk watchdog (#1635/A11 round 6, narrowed AC1,
	// ADR-022 amendment 2026-09-15; round 8 correction, same amendment date):
	// Nightgauge never seeds or merges into an operator-owned OpenCode config
	// directory (OPENCODE_CONFIG_DIR, or $HOME/.opencode when
	// opencode.inherit_user_config leaves HOME untouched — since #1787, a
	// non-inheriting run's own per-run HOME keeps $HOME/.opencode
	// structurally out of reach, so only the inherited case can still arm
	// this watchdog), so offline — or against an unreachable registry —
	// OpenCode's own install into one can block the CLI before it ever
	// prints a byte: no step_start, so the plugin handshake above never even
	// runs, and nothing on stderr says why. Bounded independently of the
	// stage's own timeout (opts.Timeout can be minutes; this cannot be, or
	// "never a hang" is only true in the limit).
	//
	// operatorInstallRisk (adapters.operatorInstallRisk) is only ever
	// non-empty here for a directory opencodeplugin.OperatorInstallSatisfied
	// reported UNSATISFIED at spawn time — a directory already satisfied
	// never arms this watchdog at all (round 8: it gets OpenCode's own local,
	// instant fast path, same as a run's own XDG-resolved config directory,
	// so there is nothing to bound). Once armed, the watchdog stands down on
	// EITHER of two independent pieces of evidence that OpenCode's own
	// install is no longer in the way, whichever arrives first: the directory
	// BECOMING satisfied — polled, read-only, never written by this goroutine
	// — or ANY output arriving on stdout or stderr, proof the CLI is not
	// stuck before its first line. The poll exists because an operator with a
	// reachable registry has OpenCode's own install completing in the
	// background while the CLI itself stays silent until its first model
	// step; without polling for satisfaction, a slow first token (a local
	// model prefilling a large prompt) would otherwise still be capped by
	// this watchdog even though the install it exists to bound is long done —
	// the exact latency-capping regression round 8 exists to fix. Whatever
	// happens after stand-down is the existing handshake/parser logic's job,
	// not this watchdog's. killProcessTreeUntilGone, the same group-kill the
	// handshake failure path uses (its own doc comment explains why a single
	// SIGKILL can miss a grandchild forked in the same instant), not a bare
	// context timeout: the default exec.CommandContext cancellation signals
	// only the direct process, and a still-running npm child inheriting the
	// stdout/stderr pipes would otherwise keep wg.Wait() below blocked past
	// this bound regardless.
	//
	// Deliberately NOT one of wg's two members: wg.Wait() below gates on the
	// stdout/stderr readers alone, so this watchdog can be told to stop
	// (stopOperatorInstallWatchdog) once they finish on their own — a
	// process that exited quickly for an unrelated reason must not sit
	// misclassified as an install-risk timeout for the rest of the bound. A
	// member of wg here would deadlock: nothing could close that stop
	// channel before wg.Wait() itself returned.
	firstOutput := make(chan struct{})
	var firstOutputOnce sync.Once
	stopOperatorInstallWatchdog := make(chan struct{})
	operatorInstallWatchdogDone := make(chan struct{})
	var operatorInstallTimedOut atomic.Bool
	if operatorInstallRisk != "" {
		dl, hasDeadline := execCtx.Deadline()
		bound := operatorInstallWaitBound(
			openCodeOperatorInstallWaitBound,
			dl,
			hasDeadline,
			time.Now(),
		)
		riskDir := operatorInstallRisk
		go func() {
			defer close(operatorInstallWatchdogDone)
			deadline := time.After(bound)
			ticker := time.NewTicker(openCodeOperatorInstallPollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-firstOutput:
					return
				case <-stopOperatorInstallWatchdog:
					return
				case <-ticker.C:
					if opencodeplugin.OperatorInstallSatisfied(riskDir) {
						return
					}
				case <-deadline:
					operatorInstallTimedOut.Store(true)
					killProcessTreeUntilGone(cmd.Process, openCodeHandshakeKillWindow)
					return
				}
			}
		}()
	} else {
		close(operatorInstallWatchdogDone)
	}
	eachLine := func(r io.Reader, name string, onLine func([]byte), onOversize func(head, tail []byte)) {
		// firstOutputOnce fires on the very first line either stream
		// produces, oversized lines included (onOversize below still routes
		// through onLine's own drift notice) — the operator-install-risk
		// watchdog above only cares that the CLI is not silently stuck, not
		// what it first said.
		onLine = func(wrapped func([]byte)) func([]byte) {
			return func(b []byte) {
				firstOutputOnce.Do(func() { close(firstOutput) })
				wrapped(b)
			}
		}(onLine)
		if openCode != nil {
			_ = forEachLine(r, streamLineLimit, onLine, func(head, tail []byte) {
				firstOutputOnce.Do(func() { close(firstOutput) })
				openCode.stream.Drift("dropped a %s line longer than the %d-byte line limit", name, streamLineLimit)
				if onOversize != nil {
					onOversize(head, tail)
				}
			})
			return
		}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), streamLineLimit)
		for scanner.Scan() {
			onLine(scanner.Bytes())
		}
	}
	keepStderr := func(line []byte) {
		line = redactOut(line)
		stderrBuf = append(stderrBuf, line...)
		stderrBuf = append(stderrBuf, '\n')
		if opts.Streamer != nil {
			opts.Streamer.OnOutput("stderr", append(line, '\n'))
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		// Deterministic phase inference (Issue #3760): some stages (notably the
		// edit-heavy feature-dev) don't reliably emit phase markers, so infer
		// progress from observed tool activity. No-op for self-reporting stages;
		// monotonic; real markers take precedence via ObserveRealMarker.
		inferer := NewPhaseInferer(opts.Stage)
		started := false
		eachLine(stdout, "stdout", func(raw []byte) {
			line := redactOut(raw)
			stdoutBuf = append(stdoutBuf, line...)
			stdoutBuf = append(stdoutBuf, '\n')
			lineStr := string(line)
			// Parse NDJSON for token usage (adapter-specific format)
			event, stepAdded := tokenAcc.ParseLine(streamFmt, lineStr)
			if stepAdded && costCap.observe(tokenAcc) {
				fmt.Fprintf(os.Stderr, "%s#%d %s: %s\n", opts.Repo, opts.IssueNumber, opts.Stage, costCap.notice())
				costCap.stop(cmd.Process, execution.done)
			}
			// Nightgauge OpenCode plugin handshake (#1635): step_start is the
			// earliest point at which a tool call could possibly exist, so
			// checking the instant the FIRST one is observed proves the
			// plugin's init (and so its sentinel write) happened before any
			// tool ran. A failure here kills the process group at once —
			// the marker itself is appended to stderr only after wg.Wait()
			// (below), never from this goroutine, so it never races the
			// stderr goroutine's own append to the same buffer.
			if pluginHandshakeOK && event != nil {
				switch event.Type {
				case "step_start":
					if !handshakeChecked {
						handshakeChecked = true
						if err := opencodeplugin.VerifyLoaded(pluginHandshake); err != nil {
							// Kill the process group FIRST: openCodePluginHandshakeMarker
							// runs `opencode --version` (bounded at 5s, but
							// observed to take real, non-zero time) to name the
							// binary in the failure marker. Computing that
							// marker before signalling used to leave the
							// stage's process group alive and ungated for the
							// probe's whole duration — exactly the window a
							// failed handshake must close first (ADR-022,
							// #1635 fix round finding 3).
							//
							// killProcessTreeUntilGone, not a single
							// signalProcessTree call: a single SIGKILL can miss
							// a grandchild the stage forks in the same instant
							// the signal is delivered — the fork completing
							// after the kernel already decided who receives a
							// pending group signal. Linux aborts a fork under a
							// pending group kill; XNU (macOS) does not, so the
							// new child joins the group, survives, keeps
							// stdout open, and the stage hangs until it exits
							// on its own (#1635 fix round finding 6, observed
							// with opencode's own child-spawn timing: the first
							// tool's child is typically forked within a few ms
							// of step_start, i.e. right when this fires).
							killProcessTreeUntilGone(cmd.Process, openCodeHandshakeKillWindow)
							handshakeFailure = openCodePluginHandshakeMarker(err, cmd.Path)
						}
					}
				case "tool_use":
					if firstToolUseAt.IsZero() {
						// Prefer the tool's own start time (part.state.time.start,
						// epoch milliseconds, ParseOpenCodeStreamLine's
						// event.OpenCodeToolStartedAt) over this goroutine's
						// wall clock: 1.18.30 emits tool_use only once a call
						// has completed or errored, so "now" is always later
						// than when the tool actually started, and a
						// sentinel written in between would wrongly read as
						// on time (#1635 fix round finding 4).
						firstToolUseAt = time.Now()
						if event.OpenCodeToolStartedAt > 0 {
							firstToolUseAt = time.UnixMilli(event.OpenCodeToolStartedAt)
						}
					}
				}
			}
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
		}, nil)
	}()
	go func() {
		defer wg.Done()
		var onOversize func(head, tail []byte)
		if openCode != nil {
			onOversize = func(head, tail []byte) {
				if notice, kind := openCode.observeStderr(string(head), string(tail)); kind == stderrNotice {
					keepStderr([]byte(notice))
				}
			}
		}
		eachLine(stderr, "stderr", func(raw []byte) {
			if openCode != nil {
				// The notice is read on the line as printed: redaction can
				// rewrite the end it is recognized by.
				switch notice, kind := openCode.observeStderr(string(raw), string(raw)); kind {
				case stderrDropped:
					return
				case stderrNotice:
					raw = []byte(notice)
				}
			}
			keepStderr(raw)
		}, onOversize)
	}()

	// Wait for output to drain, then wait for process
	wg.Wait()
	// The two readers are done — the process exited (or was killed) on its
	// own, so the operator-install-risk watchdog above no longer needs to
	// wait out the rest of its bound: tell it to stop, and wait for it to
	// actually have (operatorInstallTimedOut below is a single-writer,
	// read-after-barrier fact exactly like handshakeChecked/firstToolUseAt).
	close(stopOperatorInstallWatchdog)
	<-operatorInstallWatchdogDone
	// A stage the cost watchdog stopped ends its stderr with the marker
	// failure classification reads, added once both readers are done.
	if costCap != nil && costCap.fired {
		keepStderr([]byte(costCap.notice()))
	}
	// Nightgauge OpenCode plugin handshake, exit check (#1635): re-stat the
	// sentinel now the run has ended. Its mtime must precede the first
	// tool_use this stream observed — a step_start check alone cannot see a
	// sentinel written AFTER a tool already ran (a race the step_start check
	// resolves in the plugin's favor, since nothing had happened yet to
	// gate). Skipped once the step_start check already failed: the marker is
	// already set, and re-deriving the same verdict would only risk masking
	// it with a different one.
	if pluginHandshakeOK && handshakeFailure == "" {
		if err := opencodeplugin.VerifyNotLate(pluginHandshake, firstToolUseAt); err != nil {
			handshakeFailure = openCodePluginHandshakeMarker(err, cmd.Path)
		}
	}
	// Operator install risk, classified (#1635/A11 round 6, narrowed AC1):
	// the watchdog above killed the stage because it produced no output at
	// all within the bound, while its config touched an operator-owned
	// OpenCode directory that did not already satisfy the pin — the known
	// shape of OpenCode's own install waiting on an unreachable registry. A
	// genuine handshake failure is more specific and wins (handshakeFailure
	// == "" guards it); an operator's own Stop is not this and must not be
	// misreported as one (execution.stopRequested guards it — CancelWithGrace
	// sets it before this goroutine barrier is ever reached).
	if handshakeFailure == "" && operatorInstallTimedOut.Load() && !execution.stopRequested.Load() {
		handshakeFailure = openCodePluginHandshakeMarker(&opencodeplugin.IncompatibleError{
			Reason: fmt.Sprintf(
				"opencode produced no output at all: OpenCode's own @opencode-ai/plugin install into the operator-owned OpenCode config directory %s may be waiting on an unreachable registry. "+
					"Nightgauge never seeds or merges into an operator-owned OpenCode directory (ADR-022 amendment 2026-09-15) — "+
					"pre-warm it online, remove it, or turn off opencode.inherit_user_config. nightgauge/nightgauge#1787 gave a non-inheriting run its own per-run HOME, so this can only happen with opencode.inherit_user_config on",
				operatorInstallRisk,
			),
		}, cmd.Path)
	}
	if handshakeFailure != "" {
		keepStderr([]byte(handshakeFailure))
	}
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

	// OpenCode's usage fold reads its subagent sessions into tokenAcc before
	// the result is built from it; the rest of what it learned is applied on
	// top of the result. It runs under ctx rather than execCtx, so a stage
	// that timed out still has its usage read, from the run's root rather
	// than the worktree, and not at all once the operator stopped the stage.
	var openCodeDone *openCodeOutcome
	if openCode != nil {
		exit := openCodeExit{
			bin:        cmd.Path,
			env:        cmd.Env,
			runRoot:    os.TempDir(),
			exitCode:   -1,
			dispatched: openCodeModel,
			stopped:    execution.stopRequested.Load(),
		}
		if runOpts.RunRoot != nil && runOpts.RunRoot.Dir != "" {
			exit.runRoot = runOpts.RunRoot.Dir
		}
		if runOpts.RunRoot != nil {
			exit.endpoints = runOpts.RunRoot.Endpoints
		}
		if cmd.ProcessState != nil {
			exit.exitCode = cmd.ProcessState.ExitCode()
		}
		done := openCode.finish(ctx, exit, tokenAcc)
		openCodeDone = &done
		// The fold added the subagent sessions' usage, which the stream never
		// carried: a stage they took past its cost budget fails too. So does a
		// stage that would otherwise succeed but whose subagent usage was only
		// partly read, since its budget cannot be verified; one the operator
		// stopped read none of it by design and is cancelled, not failed.
		unread := done.partial && !exit.stopped && exit.exitCode == 0 && done.marker == ""
		if costCap.settle(tokenAcc, unread) {
			fmt.Fprintf(os.Stderr, "%s#%d %s: %s\n", opts.Repo, opts.IssueNumber, opts.Stage, costCap.notice())
			keepStderr([]byte(costCap.notice()))
		}
	}
	result := runResultFromAccumulator(string(stdoutBuf), string(stderrBuf), tokenAcc, modelTracker)
	if openCodeDone != nil {
		openCodeDone.apply(result)
		openCodeDone.report(opts)
	}
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
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			result.ExitCode = exitErr.ExitCode()
		case stoppedStageExited(result.Cancelled, cmd.ProcessState, err):
			result.ExitCode = cmd.ProcessState.ExitCode()
		default:
			return result, fmt.Errorf("wait: %w", err)
		}
	}
	// A stage stopped at its cost budget failed, even when it exited 0 on the
	// SIGTERM, and so did one its subagents took past it or whose subagent
	// usage was only partly read.
	if costCap != nil && costCap.fired && result.ExitCode == 0 {
		result.ExitCode = 1
	}
	// A failed Nightgauge OpenCode plugin handshake (#1635) must fail the
	// stage even when the CLI itself exited 0: the step_start check kills the
	// process group, but a CLI that traps the signal and still exits 0 (or a
	// handshake that only failed at the exit-time late-sentinel recheck,
	// VerifyNotLate, after the process had already exited cleanly) must not
	// read as a successful stage. Before this, only handshakeFailure's marker
	// text reached stderr; ExitCode was never forced, so a run whose plugin
	// never loaded, or loaded late, could still report success (#1635 fix
	// round finding 4).
	if handshakeFailure != "" && result.ExitCode == 0 {
		result.ExitCode = 1
	}

	if opts.Streamer != nil {
		opts.Streamer.OnComplete(*result)
	}

	return result, nil
}

// stoppedStageExited reports whether cmd.Wait's error is only the stop's own
// context cancellation, on a stage the operator stopped whose process has
// been reaped (#1627).
//
// A stop signals the stage's group, waits out its grace, and only then
// cancels the stage's context (StopExecution, CancelWithGrace). When the
// stage traps the SIGTERM and exits 0 while something it started still holds
// its output past the grace, the stage is an unreaped zombie when the context
// is cancelled: os/exec's context watcher signals it, the signal succeeds,
// and cmd.Wait then returns the context's error instead of the exit status it
// reaped. The stage did not fail to be waited for. It was stopped, which
// RunResult.Cancelled already says (#564). Reported as a wait error it was
// lost: the scheduler's runner drops the result on an error return, so the
// operator's stop was classified as the stage's own failure.
func stoppedStageExited(stopped bool, state *os.ProcessState, err error) bool {
	return stopped && state != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
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
		PeakStepInputTokens:   tokenAcc.PeakStepInputTokens,
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
// openCodePluginHandshakeMarker renders a failed Nightgauge OpenCode plugin
// handshake check (#1635) as the stage's stderr marker. err is an
// *opencodeplugin.IncompatibleError; its own Error() already carries the bare
// word "adapter_incompatible" the terminalkind table keys on (see
// internal/orchestrator/failure_handler.go's TerminalKindAdapterIncompatible),
// so this only adds the observed `opencode --version`, best-effort, naming
// the binary the handshake failed against.
func openCodePluginHandshakeMarker(err error, ocBinary string) string {
	version := "(unknown)"
	if ocBinary != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, verr := exec.CommandContext(ctx, ocBinary, "--version").Output(); verr == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				version = v
			}
		}
	}
	return fmt.Sprintf("[nightgauge-opencode-plugin] %v (opencode --version: %s)", err, version)
}

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

// openCodeHandshakeKillWindow bounds killProcessTreeUntilGone's re-signal
// loop. The race it closes resolves within single-digit milliseconds in
// practice (the escaping child is one already mid-fork when the first
// SIGKILL lands, observed 3-8ms after the group's leader emits the
// step_start line this handshake reacts to), so this only needs to be long
// enough that a slow CI host is never mistaken for an unkillable process.
// killProcessTreeUntilGone cannot detect the race resolving and return
// early (see its own doc comment), so every call runs the full window: this
// is a fixed per-failure cost, not a ceiling on the common case.
const openCodeHandshakeKillWindow = 1 * time.Second

// openCodeOperatorInstallWaitBound bounds the operator-install-risk
// watchdog: the longest a dispatch waits — with neither the target directory
// becoming satisfied nor any output at all arriving — before killing the
// stage and classifying it, once its config touches an operator-owned
// OpenCode directory opencodeplugin.OperatorInstallSatisfied reported
// UNSATISFIED at spawn time (#1635/A11 round 6, ADR-022 amendment
// 2026-09-15, narrowed AC1; round 8 correction, same amendment date: a
// directory already satisfied at spawn time never arms this watchdog at
// all — see operatorInstallRisk's own doc comment). A variable, not a
// constant, so a test can shorten it (the same pattern openCodeProbeTimeout
// uses) — and further capped at dispatch time by whatever remains of the
// stage's own context deadline, so this is never the LONGER of the two.
//
// 100s: an UNSATISFIED $HOME/.opencode or OPENCODE_CONFIG_DIR needs OpenCode
// to actually run its own real @opencode-ai/plugin install over the
// network — a genuine registry round trip, not merely a local check — so
// even with a reachable registry this can legitimately take tens of
// seconds. 100s leaves headroom above that intrinsic, legitimate delay so
// narrowed AC1's online case — an operator with a reachable registry,
// exactly as their own OpenCode runs — is not cut short by this bound
// (the watchdog also stands down the moment the directory becomes satisfied,
// well before this bound in that case), while still catching the truly
// unbounded case (ADR-022's amendment records 71s-146.88s waits against an
// UNREACHABLE registry, and an unresolvable one waits far longer than
// that). nightgauge/nightgauge#1787 gave a non-inheriting run its own
// per-run HOME, so $HOME/.opencode is no longer a config directory at all
// for one; this bound now only matters under
// opencode.inherit_user_config, or for OPENCODE_CONFIG_DIR, which that
// setting still leaves pointed at the operator's own.
var openCodeOperatorInstallWaitBound = 100 * time.Second

func operatorInstallWaitBound(
	configured time.Duration,
	deadline time.Time,
	hasDeadline bool,
	now time.Time,
) time.Duration {
	if !hasDeadline {
		return configured
	}
	return min(configured, max(deadline.Sub(now), 0))
}

// openCodeOperatorInstallPollInterval is how often the operator-install-risk
// watchdog re-checks, read-only, whether its target directory has become
// satisfied while it waits (#1635/A11 round 8, ADR-022 amendment
// 2026-09-15). Short enough that a legitimate install completing in the
// background is noticed promptly — an armed watchdog must never cap model
// latency once OpenCode's own install is done — and cheap enough (a handful
// of os.Stat calls) that polling it costs nothing next to the seconds this
// watchdog waits. A variable, not a constant, so a test can shorten it the
// same way openCodeOperatorInstallWaitBound already is.
var openCodeOperatorInstallPollInterval = 1500 * time.Millisecond

// killProcessTreeUntilGone repeatedly signals proc's whole process group
// with SIGKILL, spaced a short interval apart, for the full window — it
// cannot detect the group being gone and return early (see below), so it
// always runs to completion.
//
// A single SIGKILL is not enough for the #1635 plugin-handshake failure path
// on darwin/XNU: opencode's shell leader was observed to fork its first
// tool's child within a few milliseconds of the step_start line this
// handshake check reacts to — i.e. right when the SIGKILL is sent. Linux
// aborts a fork that lands under a pending group-kill signal; XNU does not,
// so the new grandchild can complete its fork AFTER the kernel already
// decided the pending signal had no takers, join the group, and keep
// running — inheriting the stage's stdout, so the manager's stream-reading
// goroutine (and so RunStage) blocks until that process exits on its own
// (#1635 fix round finding 6; reproduced on this darwin development machine
// in roughly one run in three of
// TestOpenCodePluginHandshakeFailureKillsTheStage before this fix — CI runs
// on Linux, where a fork aborts under a pending group signal, so it was
// likely green throughout).
//
// The exit check cannot be "kill(-pgid, 0) returns ESRCH": the stage's own
// process is not reaped until the manager's later cmd.Wait(), so it stays a
// zombie — still a live member of its own process group as far as the
// kernel's signal-delivery bookkeeping is concerned — for the entire time
// this function can run, which means kill(-pgid, 0) never reports ESRCH here
// (observed returning EPERM on darwin instead, well before any deadline).
// So instead this sends a bounded, closely-spaced burst of SIGKILLs: cheap
// and more than enough to catch a fork landing within a few milliseconds of
// the first one, without blocking a non-racing kill on a signal that will
// never arrive.
func killProcessTreeUntilGone(proc *os.Process, window time.Duration) {
	if proc == nil {
		return
	}
	const interval = 15 * time.Millisecond
	deadline := time.Now().Add(window)
	for {
		signalProcessTree(proc, syscall.SIGKILL)
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(interval)
	}
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
// the host environment less the variables the adapter withholds, plus the
// adapter's own exports, plus the run-scoped exports the manager owns.
//
// Extracted from RunStage for the same reason buildRunOptions was. This env IS
// the interface between this process and the child; inline in a function whose
// next statement spawns a process, it was assertable only by spawning one.
func composeStageEnv(base []string, withhold func(key string) bool, adapterEnv map[string]string, skillPath, runID string) []string {
	// Deterministic Node for the stage subprocess (#3863): a non-interactive
	// spawn does not inherit the login shell's nvm PATH, so resolve Node from
	// the host's nvm `default` alias and prepend it. No-op when node is already
	// on PATH (hosted runners) or unresolvable.
	env, _ := applyNodeResolution(base)
	// Withheld means not inherited: the host's value is removed before the
	// adapter's exports are added, and the decision reads only the name, so no
	// value is read or logged. Removal, not an empty value, because a reader
	// may test presence. The adapter's exports come after, so an export whose
	// name the adapter withholds from the host still arrives.
	if withhold != nil {
		kept := env[:0:0]
		for _, kv := range env {
			key, _, _ := strings.Cut(kv, "=")
			if !withhold(key) {
				kept = append(kept, kv)
			}
		}
		env = kept
	}
	// Upserted, not appended: an export replaces the host's value of the same
	// name rather than landing beside it with precedence left to the reader.
	// The opencode adapter re-points XDG_CONFIG_HOME, GH_CONFIG_DIR and
	// NIGHTGAUGE_CONFIG_HOME, and a stale inherited copy must not survive.
	for k, v := range adapterEnv {
		env = upsertEnvVar(env, k, v)
	}

	// Export the running binary so skill subprocesses discover it under any
	// adapter via $NIGHTGAUGE_BIN — the skill's PREFLIGHT cascade honors
	// this first, then prepends its dir to PATH for bare `nightgauge …`
	// calls. This removes the need for any VSCode-extension-specific binary
	// path in skills (Issue #4029). Upserted (not appended) so the host value is
	// authoritative — no duplicate NIGHTGAUGE_BIN with OS-dependent
	// precedence if one was inherited. Best-effort: a failure to resolve self
	// never blocks the spawn (the cascade has PATH/repo fallbacks).
	if self := hostBinaryPath(hostExecutable); self != "" {
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

// redactedSecretMinLen is the shortest environment value envValueRedactor
// treats as a secret. Every secret it exists for is far longer (the opencode
// server password is 26 characters, a GitHub token 40 or more), and a short
// value, such as a variable set to "1", would redact every occurrence of an
// ordinary string.
const redactedSecretMinLen = 8

// envValueRedactor returns a replacer that swaps the value of each variable
// in names, as env holds it, for "[REDACTED:<name>]", or nil when none of
// them holds a value worth redacting. A variable whose name says it holds a
// setting rather than a credential (isProviderSetting) is not redacted. Each
// value is matched both as it is and as the content of a JSON string
// (jsonEscaped), because a --format json event escapes a tool's output: a
// value holding a quote or a backslash, such as a JSON service key, is
// otherwise never found there. Longer forms are matched first, so a secret
// that contains another is replaced whole.
func envValueRedactor(env, names []string) *strings.Replacer {
	type secret struct{ form, name string }
	var secrets []secret
	for _, name := range names {
		value, ok := lookupEnvList(env, name)
		if !ok || len(value) < redactedSecretMinLen || isProviderSetting(name) {
			continue
		}
		secrets = append(secrets, secret{value, name})
		if escaped := jsonEscaped(value); escaped != value {
			secrets = append(secrets, secret{escaped, name})
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i].form) > len(secrets[j].form) })
	pairs := make([]string, 0, 2*len(secrets))
	for _, s := range secrets {
		pairs = append(pairs, s.form, "[REDACTED:"+s.name+"]")
	}
	return strings.NewReplacer(pairs...)
}

// isProviderSetting reports whether the variable name holds a provider setting
// rather than a credential: OpenCode's catalog binds a provider's region,
// project, account, host and endpoint to it beside its key (AWS_REGION,
// GOOGLE_VERTEX_PROJECT, DATABRICKS_HOST), and redacting their values would
// strip every "us-east-1", or an organization's name, from a stage's output.
// A name with a credential segment (KEY, APIKEY, TOKEN, SECRET, PASSWORD,
// PASSWD, PAT) is never a setting, so AWS_ACCESS_KEY_ID stays redacted; nor is
// a name of any shape not listed here, so a variable a later catalog adds is
// redacted until it is recognized. GOOGLE_APPLICATION_CREDENTIALS holds the
// path of a credential file, not the credential.
func isProviderSetting(name string) bool {
	segments := strings.Split(name, "_")
	for _, s := range segments {
		switch s {
		case "KEY", "APIKEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "PAT":
			return false
		}
	}
	if name == "GOOGLE_APPLICATION_CREDENTIALS" {
		return true
	}
	switch segments[len(segments)-1] {
	case "REGION", "LOCATION", "PROJECT", "ACCOUNT", "HOST", "ENDPOINT", "URL", "NAME", "ID":
		return true
	}
	return false
}

// lookupEnvList returns the value env, a KEY=VALUE list, holds for key: the
// last entry wins, as it does for exec.Cmd.
func lookupEnvList(env []string, key string) (string, bool) {
	prefix := key + "="
	value, found := "", false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value, found = strings.TrimPrefix(kv, prefix), true
		}
	}
	return value, found
}

// redactLine applies redact to one line of child output. With no redactor the
// scanner's bytes are returned as they are.
func redactLine(redact *strings.Replacer, line []byte) []byte {
	if redact == nil {
		return line
	}
	return []byte(redact.Replace(string(line)))
}

// CleanupOpenCodeRunRoot deletes the OpenCode per-run root of the run runID
// (ADR-022 § 22), and with it the run's session database, transcripts and
// logs. Every terminal outcome of a run calls it, whatever adapter its stages
// used, because any of them may have run on opencode. A run with no identity
// has no root to delete, and a missing root is not an error.
// adapters.RemoveOpenCodeRunRoot refuses anything that is not a root.
func (m *Manager) CleanupOpenCodeRunRoot(runID string) error {
	if runID == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("opencode run root: %w", err)
	}
	return adapters.RemoveOpenCodeRunRoot(home, runID)
}

// ExecutionInfo is a summary of a running execution (safe for serialization).
type ExecutionInfo struct {
	Repo        string        `json:"repo"`
	IssueNumber int           `json:"issueNumber"`
	Stage       string        `json:"stage"`
	PID         int           `json:"pid"`
	Duration    time.Duration `json:"duration"`
}
