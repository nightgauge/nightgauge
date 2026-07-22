package inbound

import (
	"strconv"
	"strings"
)

// PipelineCommandType is the discriminant tag for PipelineCommand. It
// classifies the inbound slash command so the TypeScript dispatcher can
// route to the matching IPC method without re-parsing the raw text.
type PipelineCommandType string

const (
	CmdStatus      PipelineCommandType = "status"
	CmdRun         PipelineCommandType = "run"
	CmdPause       PipelineCommandType = "pause"
	CmdResume      PipelineCommandType = "resume"
	CmdStop        PipelineCommandType = "stop"
	CmdQueueAdd    PipelineCommandType = "queue.add"
	CmdQueueRemove PipelineCommandType = "queue.remove"
	CmdQueueList   PipelineCommandType = "queue.list"
	CmdHealth      PipelineCommandType = "health"
	CmdHelp        PipelineCommandType = "help"
	CmdUnknown     PipelineCommandType = "unknown"
)

// PipelineCommand is a parsed slash-command payload. IssueNumber is set
// for commands that target a specific issue (run, stop, queue.add,
// queue.remove). Repo is set when a `--repo owner/slug` flag is present.
// RawText preserves the original input for audit/logging.
type PipelineCommand struct {
	Type        PipelineCommandType `json:"type"`
	IssueNumber int                 `json:"issue_number,omitempty"`
	Repo        string              `json:"repo,omitempty"`
	RawText     string              `json:"raw_text"`
}

// triggerPrefix is the leading slash-command word stripped from the
// text field when present. Mattermost outgoing-webhook payloads may
// or may not include the trigger word in `text` depending on the
// server's `Mattermost.SlashCommand.IncludeTriggerWord` setting.
const triggerPrefix = "/nightgauge"

// Parse converts the `text` field of a Mattermost slash command into
// a typed PipelineCommand. Unknown or unrecognized subcommands return
// CmdUnknown; empty input returns CmdHelp. Parse never returns an
// error — the dispatcher always receives a well-formed value.
func Parse(text string) PipelineCommand {
	cmd := PipelineCommand{RawText: text}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		cmd.Type = CmdHelp
		return cmd
	}

	// Strip the optional leading "/nightgauge" prefix so users
	// who type the trigger word inside the `text` field don't see
	// it routed as an unknown command.
	if strings.HasPrefix(strings.ToLower(trimmed), triggerPrefix) {
		trimmed = strings.TrimSpace(trimmed[len(triggerPrefix):])
		if trimmed == "" {
			cmd.Type = CmdHelp
			return cmd
		}
	}

	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		cmd.Type = CmdHelp
		return cmd
	}

	switch strings.ToLower(tokens[0]) {
	case "status":
		cmd.Type = CmdStatus
	case "run":
		cmd.Type = CmdRun
		cmd.IssueNumber, cmd.Repo = parseIssueAndRepo(tokens[1:])
	case "pause":
		cmd.Type = CmdPause
	case "resume":
		cmd.Type = CmdResume
	case "stop":
		cmd.Type = CmdStop
		// `stop` takes an optional issue number; absence means
		// "stop the first/only active execution" — the TS dispatcher
		// resolves the executionId.
		cmd.IssueNumber, _ = parseIssueAndRepo(tokens[1:])
	case "queue":
		cmd.Type, cmd.IssueNumber = parseQueueSubcommand(tokens[1:])
	case "health":
		cmd.Type = CmdHealth
	case "help":
		cmd.Type = CmdHelp
	default:
		cmd.Type = CmdUnknown
	}

	return cmd
}

// parseIssueAndRepo extracts the leading positional issue number and
// an optional `--repo owner/slug` flag. Returns zero values for
// missing fields. Non-numeric issue tokens are silently dropped so a
// typo doesn't surface as an unknown command.
func parseIssueAndRepo(args []string) (int, string) {
	var issue int
	var repo string

	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--repo" {
			if i+1 < len(args) {
				repo = args[i+1]
				i++
			}
			continue
		}
		if strings.HasPrefix(tok, "--repo=") {
			repo = strings.TrimPrefix(tok, "--repo=")
			continue
		}
		if issue == 0 {
			if n, err := strconv.Atoi(strings.TrimPrefix(tok, "#")); err == nil {
				issue = n
			}
		}
	}

	return issue, repo
}

// parseQueueSubcommand routes the `queue <verb> [N]` form to the
// matching command type. Unrecognized verbs return CmdUnknown so the
// caller can render a usage hint rather than executing the wrong
// branch.
func parseQueueSubcommand(args []string) (PipelineCommandType, int) {
	if len(args) == 0 {
		return CmdUnknown, 0
	}
	switch strings.ToLower(args[0]) {
	case "add":
		issue, _ := parseIssueAndRepo(args[1:])
		return CmdQueueAdd, issue
	case "remove", "rm":
		issue, _ := parseIssueAndRepo(args[1:])
		return CmdQueueRemove, issue
	case "list", "ls":
		return CmdQueueList, 0
	default:
		return CmdUnknown, 0
	}
}
