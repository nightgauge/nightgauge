package adapters

import (
	"strings"
)

// Conversational turns (#1569). A conversation turn is not a pipeline stage:
// it answers one posted message in a new process, with nothing persisted, its
// prior context replayed as data in the stdin message, and a permission
// posture no workspace setting can widen. BuildCommand is never reused for
// one; spike #1568 found that every pipeline builder either lacks that
// posture or carries a bypass-class flag. An adapter hosts conversations only
// through a ConversationBuilder registered in conversationBuilders, and that
// map is the single place the set of conversation-capable adapters is filled:
// executionprofile.ConversationViable reads it, so the adapters a workspace
// advertises `conversation` for and the adapters a turn can run on are one
// set by construction.

// ConversationTurnOptions is everything a conversational turn's argv may
// depend on. It holds no prompt: the message is written to stdin, never put
// on argv, where text beginning with "--" would parse as a flag. It holds no
// permission or auto-accept setting either: the permission posture is fixed
// by the builder.
type ConversationTurnOptions struct {
	// Model is the model the turn runs on, resolved on the workspace.
	Model string
	// Effort is a rung the model declares, or "" to pass no effort flag.
	Effort string
	// ThinkingOff disables extended thinking for the turn.
	ThinkingOff bool
	// Tools is the explicit list of built-in tools the turn may use. Empty
	// means none, which is what a conversation turn passes: its tools are
	// the conversation tool server's (#1570).
	Tools []string
	// MCPConfig is the path of an MCP config file naming the turn's own tool
	// server, or "" for none. It is a file the host wrote, never text from a
	// message.
	MCPConfig string
	// AllowedTools are the permission rules of the tools the turn may call
	// without being asked. Under the turn's permission mode every other
	// tool is denied.
	AllowedTools []string
	// AppendSystemPrompt is fixed text the host appends to the system
	// prompt: the conversation's tool and injection boundary. Never message
	// text, which reaches the turn only on stdin.
	AppendSystemPrompt string
}

// Stream formats a ConversationBuilder's output can be read with.
const (
	// ConversationStreamClaude is Claude Code's stream-json with partial
	// messages: text arrives as stream_event content_block_delta text_delta
	// events, read by the pipeline's own NDJSON parser.
	ConversationStreamClaude = "claude-stream-json"
)

// ConversationBuilder builds the command of one stateless conversational
// turn for an adapter.
type ConversationBuilder interface {
	// BuildConversationTurn returns the command, its arguments and the extra
	// environment of one turn.
	BuildConversationTurn(opts ConversationTurnOptions) (cmd string, args []string, env map[string]string)
	// ConversationStreamFormat names the format of the turn's stdout.
	ConversationStreamFormat() string
	// HasEffortControl reports whether the adapter takes an effort rung per
	// invocation. Without one, a turn passes none and reports none applied.
	HasEffortControl() bool
}

// conversationBuilders are the adapters that host conversational turns, by
// canonical registry name. Spike #1568 § 13 is the bar an adapter clears
// before it is added here, together with its builder and the argv contract
// test (conversation_test.go) that pins it.
var conversationBuilders = map[string]func() ConversationBuilder{
	"claude-headless": func() ConversationBuilder { return NewClaudeAdapter() },
}

// ConversationBuilder returns the conversational turn builder of the named
// adapter (a canonical name or an alias), and false when the adapter does not
// host conversational turns.
func (r *Registry) ConversationBuilder(name string) (ConversationBuilder, bool) {
	factory, ok := conversationBuilders[r.resolve(name)]
	if !ok {
		return nil, false
	}
	return factory(), true
}

// ConversationAdapters returns the canonical names of the adapters that host
// conversational turns, sorted.
func (r *Registry) ConversationAdapters() []string {
	var out []string
	for _, name := range r.Names() {
		if _, ok := conversationBuilders[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// claudeConversationForbidden are the claude flags no conversational argv may
// carry: every way to bypass or widen permissions, and every way to take
// context from this machine's own session store rather than the context read.
var claudeConversationForbidden = []string{
	"--dangerously-skip-permissions",
	"--allow-dangerously-skip-permissions",
	"--resume",
	"--continue",
	"--session-id",
	"--fork-session",
}

// ConversationForbiddenFlags returns the flags no conversational argv may
// carry for any adapter, and the --permission-mode values no turn may use.
func ConversationForbiddenFlags() (flags []string, permissionModes []string) {
	return append([]string{"--dangerously-bypass-approvals-and-sandbox"}, claudeConversationForbidden...),
		[]string{"bypassPermissions", "acceptEdits", "auto"}
}

// BuildConversationTurn builds one Claude turn (spike #1568 § 3.1): a new
// process with session persistence off, restricted mode, a permission mode
// that denies anything not explicitly allowed without prompting, an explicit
// built-in tool list, no MCP server but the turn's own tool server, only the
// tools allowed by name, and partial messages so the reply streams. The
// message and the replayed context arrive on stdin.
func (a *ClaudeAdapter) BuildConversationTurn(opts ConversationTurnOptions) (string, []string, map[string]string) {
	args := []string{
		"-p",
		"--no-session-persistence",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		// --restricted refuses bypassPermissions and
		// --dangerously-skip-permissions at startup, before any model call,
		// and ignores user, project and local settings files.
		"--restricted",
		"--permission-mode", "dontAsk",
		"--tools", strings.Join(opts.Tools, ","),
		"--strict-mcp-config",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.MCPConfig != "" {
		args = append(args, "--mcp-config", opts.MCPConfig)
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	env := map[string]string{}
	if opts.ThinkingOff {
		env["CLAUDE_CODE_DISABLE_THINKING"] = "1"
	}
	return "claude", args, env
}

// ConversationStreamFormat is Claude Code's stream-json.
func (a *ClaudeAdapter) ConversationStreamFormat() string { return ConversationStreamClaude }

// HasEffortControl is true: claude takes --effort per invocation.
func (a *ClaudeAdapter) HasEffortControl() bool { return true }
