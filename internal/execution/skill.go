// skill.go builds the agent prompt from an already-composed skill body.
//
// Locating, parsing, include expansion, overlay resolution, and path rewriting
// all moved to internal/skillrender (#78) — that package is the ONE composer,
// and duplicating any of it here is the drift liability #78 exists to remove.
// This file now only assembles the prompt around an already-composed body.
package execution

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/state"
)

// BuildPrompt constructs the prompt to pass to the AI agent via stdin.
// Matches the TypeScript buildStagePrompt() behavior.
//
// Stable-prefix-first ordering (#3805): the skill body is byte-identical across
// issues for a given stage, so it is written first to form the cacheable
// prefix; the variable invocation context block trails it. This mirrors the TS
// builder's logical ordering (stable body, "---", variable trailer) — the two
// builders are aligned on ordering, not byte-identical strings (ADR-001).
//
// skillDir (when non-empty) is the absolute directory of the resolved
// SKILL.md: skill-relative read directives are rewritten against it so they
// resolve from cross-repo worktrees (#196). The rewrite is host-constant per
// stage, so the body stays byte-identical across issues (cache-safe).
func BuildPrompt(stage state.PipelineStage, skillContent string, issueNumber int, skillDir string, effectiveContextType string, contextFile string) string {
	var sb strings.Builder

	// Stable skill body first — forms the cacheable prefix (#3805).
	//
	// skillContent arrives ALREADY rewritten by skillrender.Render (ADR 016 §4
	// moves absolute-path rewriting into the render step). Rewriting again here
	// would not be a harmless no-op: the rewrite is NOT idempotent, because an
	// already-absolute "/abs/skills/_shared/" still contains the
	// "skills/_shared/" needle and a second pass expands it to
	// "/abs//abs/skills/_shared/". Every read directive would then point at a
	// path that does not exist — the #196 failure this rewrite exists to fix.
	sb.WriteString(skillContent)
	sb.WriteString("\n\n---\n\n")

	// Variable invocation context last (matches TS headless mode injection).
	sb.WriteString("## Invocation Context\n\n")
	sb.WriteString("- **Mode**: headless (non-interactive pipeline execution)\n")
	sb.WriteString(fmt.Sprintf("- **Issue**: #%d\n", issueNumber))
	sb.WriteString(fmt.Sprintf("- **Stage**: %s\n", stage))
	if effectiveContextType != "" {
		sb.WriteString(fmt.Sprintf("- **Input context type**: %s\n", effectiveContextType))
	}
	if contextFile != "" {
		sb.WriteString(fmt.Sprintf("- **Input context file**: %s\n", contextFile))
	}
	if skillDir != "" {
		sb.WriteString(fmt.Sprintf("- **Skill directory**: %s — supporting files (_includes/, _shared/) live here, NOT under the current working directory; never scan the filesystem for them (#196)\n", skillDir))
	}
	sb.WriteString("- **AskUserQuestion**: DISABLED — fail fast if undecidable\n")
	sb.WriteString("- **Auto-accept**: All tool calls are auto-approved\n")

	return sb.String()
}
