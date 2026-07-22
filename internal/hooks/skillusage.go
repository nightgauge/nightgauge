package hooks

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/skills"
)

// skillUsageInput is the PreToolUse(Skill) payload, plus the session/cwd context
// Claude Code includes at the top level of the hook JSON.
type skillUsageInput struct {
	ToolName  string          `json:"tool_name"`
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// skillToolInput is the Skill tool's input. The canonical field is "skill";
// "name" is accepted as a defensive fallback.
type skillToolInput struct {
	Skill string `json:"skill"`
	Name  string `json:"name"`
}

// LogSkillUsage records a Skill-tool invocation to the in-repo usage log and
// always returns Allow. This hook is telemetry only — it never blocks the tool,
// and any write error degrades to a stderr warning so a logging failure can
// never stall a session. The working directory is resolved from the hook's `cwd`
// field, falling back to the process cwd.
func LogSkillUsage(inputJSON []byte) GateDecision {
	var in skillUsageInput
	if err := json.Unmarshal(inputJSON, &in); err != nil {
		return Allow()
	}
	// Only log Skill invocations; allow anything else through untouched.
	if in.ToolName != "" && in.ToolName != "Skill" {
		return Allow()
	}

	var ti skillToolInput
	_ = json.Unmarshal(in.ToolInput, &ti)
	skill := ti.Skill
	if skill == "" {
		skill = ti.Name
	}
	if skill == "" {
		return Allow() // nothing identifiable to log
	}

	root := in.CWD
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}

	if err := skills.AppendRecord(root, skills.Record{Skill: skill, Session: in.SessionID}); err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to log skill usage: %v\n", err)
	}
	return Allow()
}
