/**
 * codexSandbox — maps a stage's declared `allowed-tools` onto Codex's actual
 * security controls (`--sandbox` mode + `--ask-for-approval` policy).
 *
 * Claude enforces per-stage tool permissions via `--allowedTools`. Codex has NO
 * per-invocation tool-allowlist flag — its security model is the filesystem
 * sandbox mode plus an approval policy. Historically every Codex stage ran with
 * `--dangerously-bypass-approvals-and-sandbox` (no sandbox, no approval), so the
 * stage-level boundary that exists for Claude was absent for Codex. This module
 * derives the tightest sandbox a stage's tools justify.
 *
 * SAFETY: the mapping only ever TIGHTENS with positive evidence. With no
 * `allowed-tools` (or any tool that implies shell / network / arbitrary access),
 * it returns `danger-full-access` — the prior behavior — so an autonomous run is
 * never locked out of access it needs. Autonomous runs keep
 * `--ask-for-approval never`; only the sandbox is scoped.
 *
 * @see Issue #4026 - Map skill allowed-tools → Codex sandbox mode + approval policy
 * @see https://developers.openai.com/codex (sandbox modes / approval policy)
 */

/** Codex filesystem sandbox modes, tightest → loosest. */
export type CodexSandboxMode = "read-only" | "workspace-write" | "danger-full-access";

/**
 * Tools that imply shell, network, or otherwise arbitrary access — any of these
 * forces `danger-full-access` (the stage can run `git`/`gh`/`npm`, reach the
 * network, or write outside the workspace). MCP tools (`mcp__*`) are opaque, so
 * they are treated as full-access too. Matched by base name (entries may carry
 * argument scopes, e.g. `Bash(git *)`).
 */
const FULL_ACCESS_TOOLS = new Set(["Bash", "Task", "WebFetch", "WebSearch"]);

/** Tools that mutate files but need neither shell nor network → `workspace-write`. */
const WRITE_TOOLS = new Set(["Write", "Edit", "MultiEdit", "NotebookEdit"]);

/** Extract the base tool name from an allowed-tools entry (strips `(...)` scope). */
function baseToolName(entry: string): string {
  const trimmed = entry.trim();
  const paren = trimmed.indexOf("(");
  return (paren === -1 ? trimmed : trimmed.slice(0, paren)).trim();
}

/**
 * Resolve the sandbox mode a stage's `allowed-tools` justifies.
 *
 * Returns `danger-full-access` when there is no positive evidence the run is
 * safe to constrain (empty/undefined tools, any shell/network/arbitrary tool).
 */
export function resolveCodexSandboxMode(allowedTools?: readonly string[]): CodexSandboxMode {
  if (!allowedTools || allowedTools.length === 0) {
    return "danger-full-access";
  }

  const names = allowedTools.map(baseToolName).filter((n) => n.length > 0);
  if (names.length === 0) {
    return "danger-full-access";
  }

  const needsFullAccess = names.some(
    (name) => FULL_ACCESS_TOOLS.has(name) || name.startsWith("mcp__")
  );
  if (needsFullAccess) {
    return "danger-full-access";
  }

  const needsWrite = names.some((name) => WRITE_TOOLS.has(name));
  return needsWrite ? "workspace-write" : "read-only";
}

/**
 * The Codex CLI flags for a sandbox mode on the `exec` (non-resume) path.
 *
 * `danger-full-access` uses the single `--dangerously-bypass-approvals-and-sandbox`
 * flag (the documented "ephemeral, fully sandboxed CI environment" mode — no
 * sandbox, no approvals). Tighter modes use explicit `--sandbox <mode>` with
 * `--ask-for-approval never` so autonomous runs still never block on a prompt.
 */
export function codexSandboxFlags(mode: CodexSandboxMode): string[] {
  if (mode === "danger-full-access") {
    return ["--dangerously-bypass-approvals-and-sandbox"];
  }
  return ["--sandbox", mode, "--ask-for-approval", "never"];
}

/** The full-access sentinel flag swapped out when a tighter profile applies. */
export const CODEX_BYPASS_FLAG = "--dangerously-bypass-approvals-and-sandbox";

/**
 * Apply the resolved sandbox profile to a Codex `exec` arg list. When the tools
 * justify a tighter mode, the `--dangerously-bypass-approvals-and-sandbox`
 * sentinel is replaced in place with the scoped flags. When the mode is
 * full-access, or the sentinel is absent (an operator override removed it), the
 * args are returned unchanged — the mapping never loosens or force-injects.
 */
export function applyCodexSandboxProfile(
  args: readonly string[],
  allowedTools?: readonly string[]
): string[] {
  const mode = resolveCodexSandboxMode(allowedTools);
  if (mode === "danger-full-access") {
    return [...args];
  }

  const idx = args.indexOf(CODEX_BYPASS_FLAG);
  if (idx === -1) {
    // Operator override already chose its own sandbox flags — respect them.
    return [...args];
  }

  return [...args.slice(0, idx), ...codexSandboxFlags(mode), ...args.slice(idx + 1)];
}
