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
 * `--ask-for-approval never`; only the sandbox is scoped. That policy is a
 * top-level codex option, so it goes before `exec` (#1715).
 *
 * @see Issue #4026 - Map skill allowed-tools → Codex sandbox mode + approval policy
 * @see https://developers.openai.com/codex (sandbox modes / approval policy)
 */

import { isAbsolute } from "path";

import { resolveCloneLayout } from "../../context/cloneLayout.js";

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
 * The `codex exec` flags for a sandbox mode on the non-resume path.
 *
 * `danger-full-access` uses the single `--dangerously-bypass-approvals-and-sandbox`
 * flag (the documented "ephemeral, fully sandboxed CI environment" mode — no
 * sandbox, no approvals). Tighter modes use explicit `--sandbox <mode>`, with
 * the approval policy from {@link codexApprovalFlags} before `exec`.
 */
export function codexSandboxFlags(mode: CodexSandboxMode): string[] {
  if (mode === "danger-full-access") {
    return ["--dangerously-bypass-approvals-and-sandbox"];
  }
  return ["--sandbox", mode];
}

/**
 * The flags that go BEFORE `exec` for a sandbox mode. Tighter modes pin
 * `--ask-for-approval never` so autonomous runs still never block on a prompt.
 * It is a top-level codex option, not an `exec` one: codex-cli 0.145.0 refuses
 * `codex exec --ask-for-approval never` ("unexpected argument", exit 2) and
 * accepts `codex --ask-for-approval never exec` (#1715). `danger-full-access`
 * needs none; its bypass flag covers approvals. Mirrors the Go
 * `codexApprovalFlags`.
 */
export function codexApprovalFlags(mode: CodexSandboxMode): string[] {
  if (mode === "danger-full-access") return [];
  return ["--ask-for-approval", "never"];
}

/** The full-access sentinel flag swapped out when a tighter profile applies. */
export const CODEX_BYPASS_FLAG = "--dangerously-bypass-approvals-and-sandbox";

/**
 * The flags that carry a sandbox mode onto `codex exec resume` (#2342).
 *
 * `exec resume` refuses `--sandbox` ("unexpected argument", exit 2 on
 * codex-cli 0.154.0) but honours the `sandbox_mode` config key, and a resumed
 * turn takes its sandbox from the resume invocation, never from the session it
 * resumes (spike #1568 § 4.2). So a tighter mode travels as
 * `-c sandbox_mode="<mode>"`, and only `danger-full-access` resumes with the
 * bypass flag — the same split {@link codexSandboxFlags} makes on a fresh
 * start. The approval policy still goes before `exec`
 * ({@link codexApprovalFlags}).
 */
export function codexResumeSandboxFlags(mode: CodexSandboxMode): string[] {
  if (mode === "danger-full-access") {
    return [CODEX_BYPASS_FLAG];
  }
  return ["-c", `sandbox_mode=${JSON.stringify(mode)}`];
}

/**
 * Apply the resolved sandbox profile to a Codex `exec` arg list. When the tools
 * justify a tighter mode, the `--dangerously-bypass-approvals-and-sandbox`
 * sentinel is replaced in place with the scoped flags, and the approval policy
 * is put before the `exec` subcommand (before everything when there is no
 * `exec` ahead of the sentinel). When the mode is full-access, or the sentinel
 * is absent (an operator override removed it), the args are returned
 * unchanged — the mapping never loosens or force-injects.
 */
export function applyCodexSandboxProfile(
  args: readonly string[],
  allowedTools?: readonly string[],
  cwd?: string
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

  const scoped = [
    ...args.slice(0, idx),
    ...codexSandboxFlags(mode),
    ...codexCloneWritableRoot(mode, cwd),
    ...args.slice(idx + 1),
  ];
  const exec = args.slice(0, idx).indexOf("exec");
  const at = exec === -1 ? 0 : exec;
  return [...scoped.slice(0, at), ...codexApprovalFlags(mode), ...scoped.slice(at)];
}

/**
 * Makes the clone's per-clone directory (ADR-024 § 7,
 * `<git-common-dir>/nightgauge`) writable under the workspace-write sandbox.
 * Codex keeps every `.git` inside a writable root read-only, so without it a
 * stage could not store its context, plan or retro through
 * `nightgauge layout write` ("operation not permitted"). Only CLONE is added,
 * not the git directory. Mirrors the Go `codexCloneWritableRoot`.
 */
export function codexCloneWritableRoot(mode: CodexSandboxMode, cwd?: string): string[] {
  if (mode !== "workspace-write" || !cwd || !isAbsolute(cwd)) return [];
  try {
    const { clone } = resolveCloneLayout(cwd);
    return ["-c", `sandbox_workspace_write.writable_roots=[${JSON.stringify(clone)}]`];
  } catch {
    return [];
  }
}
