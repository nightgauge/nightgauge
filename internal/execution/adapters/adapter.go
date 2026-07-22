// Package adapters defines the SkillRunner interface and AI CLI adapters.
package adapters

import (
	"context"
	"io"
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

// RunOptions are the parameters for running a skill stage.
type RunOptions struct {
	SkillPath    string // Path to the SKILL.md file
	WorktreeDir  string // Working directory for the execution
	ContextFile  string // Path to context JSON from previous stage
	OutputFile   string // Path for output context JSON
	IssueNumber  int
	Repo         string
	Stage        string
	Model        string   // Optional model override
	MaxTokens    int      // Optional token budget
	AllowedTools []string // Tools allowed for this skill (from SKILL.md frontmatter)
	Prompt       string   // Built prompt to pass via stdin (for Claude adapter)
	MaxTurns     int      // Max conversation turns
	CostBudget   float64  // Max cost in USD
	TargetRepo   string   // Expected repo for skill verification (owner/repo)
}

// RunResult captures the output of a skill execution.
type RunResult struct {
	ExitCode     int
	Stdout       string
	Stderr       string
	InputTokens  int
	OutputTokens int
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
	// See docs/spikes/fable-5-behavior-porting.md §8.3.
	RefusalFallbackFrom     string
	RefusalFallbackTo       string
	RefusalFallbackCategory string
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
