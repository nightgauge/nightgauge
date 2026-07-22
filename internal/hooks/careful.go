package hooks

import (
	"encoding/json"
	"os"

	"github.com/nightgauge/nightgauge/internal/careful"
)

// carefulGateInput is the PreToolUse payload the careful gate needs.
type carefulGateInput struct {
	ToolName  string          `json:"tool_name"`
	CWD       string          `json:"cwd"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// EvaluateCarefulGate blocks production-destructive Bash commands while careful
// mode is active (the .nightgauge/careful.lock sentinel is present and
// unexpired). When careful mode is off it is a no-op (Allow). It fails open on
// any parse error so it can never wedge a session.
func EvaluateCarefulGate(inputJSON []byte) GateDecision {
	var in carefulGateInput
	if err := json.Unmarshal(inputJSON, &in); err != nil {
		return Allow()
	}
	if in.ToolName != "Bash" {
		return Allow()
	}

	root := in.CWD
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	if !careful.Active(root) {
		return Allow()
	}

	var ti BashToolInput
	if err := json.Unmarshal(in.ToolInput, &ti); err != nil || ti.Command == "" {
		return Allow()
	}
	if reason := careful.DestructiveProdReason(carefulCommands(ti.Command)); reason != "" {
		return Block("/careful is ON — blocked: " + reason + " (run `nightgauge careful off` to disable.)")
	}
	return Allow()
}

// carefulCommands tokenizes a compound shell command into the pipelines the
// careful gate inspects, using the shared cmdparse tokenizer (with wrapper
// expansion) so destructive words inside quoted prose / heredocs never reach the
// verb matcher and a command hidden behind `bash -c`/`sudo`/`xargs` is still
// classified (#4069). Each returned group is one pipeline (commands connected by
// `|`) so the SQL check is scoped correctly.
func carefulCommands(cmd string) [][]careful.Command {
	segs := ExpandWrappers(SplitSegments(cmd))
	pipes := Pipelines(segs)
	out := make([][]careful.Command, 0, len(pipes))
	for _, pipe := range pipes {
		group := make([]careful.Command, 0, len(pipe))
		for _, s := range pipe {
			group = append(group, careful.Command{Argv: s.CommandArgv(), Raw: s.Raw})
		}
		out = append(out, group)
	}
	return out
}
