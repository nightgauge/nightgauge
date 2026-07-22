package adapters

import "strings"

// Codex sandbox mapping (#4026) — mirror of the SDK `codexSandbox.ts`.
//
// Codex has no per-invocation tool-allowlist flag; its security model is the
// filesystem sandbox mode plus an approval policy. This derives the tightest
// sandbox a stage's `allowed-tools` justify. SAFETY: it only ever TIGHTENS with
// positive evidence — with no tools, or any shell/network/arbitrary tool, it
// returns danger-full-access (the prior behavior) so autonomous runs are never
// locked out of access they need.
const (
	codexSandboxReadOnly       = "read-only"
	codexSandboxWorkspaceWrite = "workspace-write"
	codexSandboxDangerFull     = "danger-full-access"
)

// codexBypassFlag is the single-flag full-access mode (no sandbox, no approval).
const codexBypassFlag = "--dangerously-bypass-approvals-and-sandbox"

// Tools implying shell / network / arbitrary access → danger-full-access.
var codexFullAccessTools = map[string]bool{
	"Bash":      true,
	"Task":      true,
	"WebFetch":  true,
	"WebSearch": true,
}

// Tools that mutate files but need neither shell nor network → workspace-write.
var codexWriteTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// codexBaseToolName strips an argument scope (e.g. "Bash(git *)" → "Bash").
func codexBaseToolName(entry string) string {
	t := strings.TrimSpace(entry)
	if i := strings.Index(t, "("); i != -1 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// resolveCodexSandboxMode returns the sandbox mode a stage's allowed-tools
// justify, defaulting to danger-full-access when there is no positive evidence
// the run is safe to constrain.
func resolveCodexSandboxMode(allowedTools []string) string {
	names := make([]string, 0, len(allowedTools))
	for _, e := range allowedTools {
		if n := codexBaseToolName(e); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return codexSandboxDangerFull
	}

	for _, n := range names {
		if codexFullAccessTools[n] || strings.HasPrefix(n, "mcp__") {
			return codexSandboxDangerFull
		}
	}
	for _, n := range names {
		if codexWriteTools[n] {
			return codexSandboxWorkspaceWrite
		}
	}
	return codexSandboxReadOnly
}

// codexSandboxFlags returns the `exec`-path flags for a sandbox mode. Tighter
// modes pin `--ask-for-approval never` so autonomous runs still never block.
func codexSandboxFlags(mode string) []string {
	if mode == codexSandboxDangerFull {
		return []string{codexBypassFlag}
	}
	return []string{"--sandbox", mode, "--ask-for-approval", "never"}
}
