// Package adapters defines the SkillRunner interface and AI CLI adapters.
package adapters

import (
	"context"
	"io"
	"strings"
)

// SkillRunner is the interface for AI CLI adapters (Claude, Codex, Gemini).
// Each adapter knows how to construct the correct command to invoke the AI CLI
// with a skill prompt.
type SkillRunner interface {
	// Name returns the adapter name (e.g., "claude", "codex", "gemini").
	Name() string

	// BuildCommand constructs the command and arguments to run a skill.
	BuildCommand(opts RunOptions) (cmd string, args []string, env map[string]string)

	// UsesStdin returns true if the adapter expects the prompt via stdin
	// (e.g., Claude uses stdin, Codex/Gemini use --prompt-file).
	UsesStdin() bool

	// Agentic reports whether this adapter drives a real agentic tool loop
	// (edit files, run shell, call gh) — a hard requirement for pipeline
	// stage dispatch (#57). Chat-completion-only paths (the ollama/lm-studio
	// bridges, whose execution bottoms out in the TypeScript fetch/SSE
	// adapters with zero tool handling) report false: a stage dispatched to
	// them emits prose instead of commits. Manager.RunStage rejects
	// non-agentic adapters before spawning; eval/judge surfaces do not.
	Agentic() bool
}

// RunIDEnvVar is the environment variable every adapter exports the run
// identity under (ADR-017). Single-sourced so the exporters, the manager's
// env composition (which strips an inherited value when a dispatch has no
// identity of its own) and the contract test all name the same key.
const RunIDEnvVar = "NIGHTGAUGE_RUN_ID"

// RunOptions are the parameters for running a skill stage.
type RunOptions struct {
	SkillPath   string // Path to the SKILL.md file
	WorktreeDir string // Working directory for the execution
	ContextFile string // Path to context JSON from previous stage
	OutputFile  string // Path for output context JSON
	IssueNumber int
	Repo        string
	Stage       string
	Model       string // Optional model override
	// Effort is the effort half of the dispatch envelope (#581/#606), an
	// EFFORT_LEVELS rung or "" (no explicit effort — the model's declared
	// default rules). Threaded from the scheduler's wire resolution so a
	// descended rung can reach the spawned CLI. Consumed today only by the
	// grok adapter (the #532/#606 effort-descent contract), where the
	// provider-global NIGHTGAUGE_GROK_EFFORT env var is demoted to operator
	// override: it wins when set, this value dispatches otherwise. Every
	// other adapter ignores the field — their effort stays env/TS-owned.
	Effort       string
	MaxTokens    int      // Optional token budget
	AllowedTools []string // Tools allowed for this skill (from SKILL.md frontmatter)
	Prompt       string   // Built prompt to pass via stdin (for Claude adapter)
	MaxTurns     int      // Max conversation turns
	CostBudget   float64  // Max cost in USD
	TargetRepo   string   // Expected repo for skill verification (owner/repo)

	// RunID is the canonical lowercase UUIDv7 run identity minted at dispatch
	// (ADR-017). Every adapter exports it to the child process as
	// RunIDEnvVar so the stage — and everything it spawns — can name the run
	// it belongs to.
	//
	// Empty EXACTLY when the dispatch is not a pipeline run (e.g. the
	// autonomous issue-refine CLI dispatch, which has no run identity by
	// construction). In that case the child receives NO run identity at all —
	// literally, not merely "this layer adds none": Manager.RunStage strips any
	// NIGHTGAUGE_RUN_ID inherited from the host environment out of the composed
	// env, so a nightgauge process that is itself running under one cannot
	// launder its identity onto an identity-less dispatch. It is never exported
	// as "" either: readers test presence, so an empty export would be adopted
	// as an identity that names nothing.
	RunID string

	// RunRoot is the per-run root the manager prepared through the adapter's
	// optional PrepareRunRoot hook, before BuildCommand. It is nil for every
	// adapter without the hook. Only the opencode adapter has one (ADR-022
	// § 8): every stage of a run shares it, and the run's end deletes it.
	RunRoot *RunRoot
}

// RunRootRequest is what the manager hands an adapter's PrepareRunRoot hook.
type RunRootRequest struct {
	// ID names the root: the run identity when the dispatch has one, or an id
	// the manager minted for this dispatch alone, which is never exported as
	// RunIDEnvVar. It always passes runstate.IsIdentity.
	ID string
	// MachineConfigDir is the directory of the machine-tier Nightgauge config
	// this process reads (config.MachineConfigDir). An adapter that moves
	// XDG_CONFIG_HOME for its child pins NIGHTGAUGE_CONFIG_HOME to it, so a
	// nightgauge command the stage runs reads the same machine tier.
	MachineConfigDir string
	// Run is the dispatch the root is prepared for, after the pre-dispatch,
	// model and effort checks. The opencode adapter builds the run's config
	// from it (ADR-022 § 8).
	Run RunOptions
	// WorkspaceRoot is the manager's workspace root, the launch root and
	// never a stage's worktree. The opencode adapter loads the config there
	// that names the GitHub identity its forge read of the MCP servers uses.
	WorkspaceRoot string
	// Context is the stage's own context, when the caller has one (the
	// manager, through RunStage). An adapter whose PrepareRunRoot hook does
	// bounded work of its own (the opencode adapter's plugin-dependency
	// seed, #1635 fix round finding 3) honours it instead of running on a
	// context.Background() disconnected from the stage's own cancellation
	// and deadline. Nil for a caller with no such context (a bare CLI verb,
	// most existing tests): the hook then falls back to
	// context.Background() and bounds its own work only by its own means.
	Context context.Context
}

// RunRoot is a directory private to one pipeline run, where an adapter whose
// CLI keeps state of its own (config, sessions, transcripts, logs) keeps it
// apart from the operator's.
type RunRoot struct {
	// Dir is the root directory.
	Dir string
	// Env points the CLI at Dir and re-pins the tools that move with it,
	// resolved against the environment this process inherited when the root
	// was prepared. BuildCommand merges it into its exports.
	Env map[string]string
	// Endpoints are the ids of the model servers the operator declared for
	// the run (the opencode adapter's OpenCodeEndpoints), each the provider
	// key a stage names on -m to reach it. The stage result's Endpoint is
	// read against them.
	Endpoints []string
}

// RunResult captures the output of a skill execution.
type RunResult struct {
	// ExitCode is the child's exit status, with one exception: a stream
	// parser that finds an exit-0 run failed reports 1. Only the opencode
	// parser does, for a run that stopped on a permission OpenCode rejected
	// by itself (ADR-022 § 9), and the marker naming it ends Stderr.
	ExitCode     int
	Stdout       string
	Stderr       string
	InputTokens  int
	OutputTokens int
	// CacheReadTokens and cache-creation fields preserve every billable token
	// pool observed in the adapter stream. CacheCreationTokens is the combined
	// total; the TTL fields carry the pricing split when available.
	CacheReadTokens       int
	CacheCreationTokens   int
	CacheCreation5mTokens int
	CacheCreation1hTokens int
	// PremiumRequests is the copilot billable unit parsed from its stats footer
	// (#52). Zero for token-metered adapters (claude/codex/gemini), which report
	// InputTokens/OutputTokens instead.
	PremiumRequests int

	// ServedModel is the model that actually served the stage per the CLI
	// stream (last observed). Empty when the stream carried no model info.
	// Differs from the requested model when the CLI's internal
	// model_refusal_fallback fires (#91) — the CLI swaps to a fallback model
	// on a safety refusal and still exits 0, so the requested model must not
	// be assumed to be the serving one.
	ServedModel string
	// RefusalFallback* echo the CLI's system/model_refusal_fallback event
	// when one was observed (#91). Attribution only — never used to retry.
	// See docs/FAILURE_TAXONOMY.md § Model Refusal Fallback.
	RefusalFallbackFrom     string
	RefusalFallbackTo       string
	RefusalFallbackCategory string

	// PeakStepInputTokens is the largest prompt a single model step sent:
	// its input plus cache-read and cache-write tokens. Unlike the summed
	// pools it does not grow with every turn, so it is what compares with a
	// context window. 0 when the stream carries no per-step usage (every
	// adapter but opencode).
	PeakStepInputTokens int
	// ModelProvider is the normalized provider of ServedModel (ADR-022 § 1),
	// which then holds the recorded form of § 2. UpstreamModel is the raw -m
	// value exactly as dispatched ("lmstudio/qwen/qwen3.8-27b"), also when
	// another model served the stage. Endpoint is the id of the declared
	// endpoint that served the model (RunRoot.Endpoints), or "" when its
	// provider key names none. Only a multi-provider adapter (opencode) sets
	// them.
	ModelProvider string
	UpstreamModel string
	Endpoint      string
	// AdapterVersion is the version the adapter's CLI reports for itself
	// (`opencode --version`), or "" when it was not read.
	AdapterVersion string
	// AdapterReportedCostUSD is the cost the CLI itself reported. OpenCode's
	// comes from its bundled catalog, not a bill, and is never used to price
	// a stage (ADR-022 § 3).
	AdapterReportedCostUSD float64
	// UsagePartial is true when part of the stage's usage could not be read,
	// such as a subagent session whose export failed. The token fields then
	// hold what was read, and DriftMarkers says what was not.
	UsagePartial bool
	// DriftMarkers are the stream parser's evidence that the CLI's output did
	// not have the shape it was written against, each prefixed
	// "[opencode-drift]". They are never success evidence.
	DriftMarkers []string

	// Cancelled is true when execution.Manager itself requested this exit —
	// CancelWithGrace/StopExecution SIGTERM'd the process and it left
	// gracefully (#564). This is the ONLY component that knows a stop was
	// asked for: a CLI that traps SIGTERM and exits 0 is indistinguishable
	// from a healthy exit on ExitCode/err alone, and the scheduler's ctx is
	// never the one CancelWithGrace cancels (the manager cancels its OWN
	// execCtx, after the process has already exited), so ctx.Err() at the
	// runner cannot see it either. Set once, here, so no second predicate for
	// "was this a stop" grows anywhere else.
	Cancelled bool
}

// openCodeToolForClaudeTool maps the Claude Code tool names a skill's
// allowed-tools frontmatter uses to the OpenCode permission governing the same
// capability. OpenCode 1.18.30 gates its write and edit tools with one
// permission, `edit`, so Write, Edit and MultiEdit all map to it. This is the
// only mapping: the stream parser's rejection markers and the permission map
// (#1638) both read it.
var openCodeToolForClaudeTool = map[string]string{
	"Bash":      "bash",
	"Read":      "read",
	"Write":     "edit",
	"Edit":      "edit",
	"MultiEdit": "edit",
	"Glob":      "glob",
	"Grep":      "grep",
	"Task":      "task",
	"WebFetch":  "webfetch",
}

// OpenCodeToolForClaudeTool returns the OpenCode permission for a Claude Code
// tool name. A frontmatter entry may carry a pattern, as "Bash(git:*)" does;
// the name before it decides.
func OpenCodeToolForClaudeTool(name string) (string, bool) {
	name, _, _ = strings.Cut(strings.TrimSpace(name), "(")
	tool, ok := openCodeToolForClaudeTool[name]
	return tool, ok
}

// OpenCodeToolsAllowed returns the set of OpenCode permissions the Claude Code
// tool names in allowed grant. A name with no OpenCode counterpart grants
// nothing.
func OpenCodeToolsAllowed(allowed []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range allowed {
		if tool, ok := OpenCodeToolForClaudeTool(name); ok {
			set[tool] = true
		}
	}
	return set
}

// OutputStreamer receives streamed output from a running skill process.
type OutputStreamer interface {
	// OnOutput is called with chunks of stdout/stderr output.
	OnOutput(stream string, data []byte)
	// OnComplete is called when the process exits.
	OnComplete(result RunResult)
}

// WriterStreamer wraps an io.Writer as an OutputStreamer.
type WriterStreamer struct {
	Writer io.Writer
}

// OnOutput writes output chunks to the wrapped writer.
func (ws *WriterStreamer) OnOutput(_ string, data []byte) {
	_, _ = ws.Writer.Write(data)
}

// OnComplete is a no-op for WriterStreamer.
func (ws *WriterStreamer) OnComplete(_ RunResult) {}

// NewNullStreamer returns a streamer that discards all output.
func NewNullStreamer() OutputStreamer {
	return &nullStreamer{}
}

type nullStreamer struct{}

func (ns *nullStreamer) OnOutput(_ string, _ []byte) {}
func (ns *nullStreamer) OnComplete(_ RunResult)      {}

// cancelableContext wraps context.WithCancel for process management.
func cancelableContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
