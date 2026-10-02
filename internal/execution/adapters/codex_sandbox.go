package adapters

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

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

// codexSandboxFlags returns the `exec` flags for a sandbox mode: the bypass
// flag for danger-full-access, `--sandbox <mode>` otherwise.
func codexSandboxFlags(mode string) []string {
	if mode == codexSandboxDangerFull {
		return []string{codexBypassFlag}
	}
	return []string{"--sandbox", mode}
}

// codexApprovalFlags returns the flags that go BEFORE `exec` for a sandbox
// mode. Tighter modes pin `--ask-for-approval never` so autonomous runs still
// never block. It is a top-level codex option, not an `exec` one: codex-cli
// 0.145.0 refuses `codex exec --ask-for-approval never` ("unexpected
// argument", exit 2) and accepts `codex --ask-for-approval never exec`
// (#1715). danger-full-access needs none; its bypass flag covers approvals.
func codexApprovalFlags(mode string) []string {
	if mode == codexSandboxDangerFull {
		return nil
	}
	return []string{"--ask-for-approval", "never"}
}

// codexCloneWritableRoot makes the clone's per-clone directory (ADR-024 § 7,
// <git-common-dir>/nightgauge) writable under the workspace-write sandbox.
// Codex keeps every `.git` inside a writable root read-only, so without it a
// stage could not store its context, plan or retro through
// `nightgauge layout write` — verified: the write fails with "operation not
// permitted" — while the working tree stays writable. Only CLONE is added,
// not the git directory. Other modes need nothing: read-only writes nowhere
// and danger-full-access has no sandbox. An unresolvable workdir adds
// nothing; the stage's own write then reports the error.
func codexCloneWritableRoot(mode, workdir string) []string {
	if mode != codexSandboxWorkspaceWrite || workdir == "" || !filepath.IsAbs(workdir) {
		return nil
	}
	clone, err := layout.CloneDir(workdir)
	if err != nil {
		return nil
	}
	return []string{"-c", fmt.Sprintf("sandbox_workspace_write.writable_roots=[%s]", strconv.Quote(clone))}
}
