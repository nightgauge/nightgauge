/**
 * Clone layout — the one place the extension addresses per-clone data.
 *
 * Mirrors the Go class resolvers of ADR-024 § 1 and § 7 (`PipelineStateDir`,
 * `PlansDir`, `RetrosDir`, `CloneLogsDir`). Every caller in the extension
 * builds a per-clone path through these helpers, so the later move of the
 * location out of the working tree (#2037) edits this module alone.
 *
 * Today the classes live in the working tree, at `<root>/.nightgauge/<class>`.
 * The helpers return exactly that, joined with `path.join`, so the result is
 * the same on every OS as the hand-built paths they replace.
 *
 * The workspace root must be a non-empty absolute path: a relative or empty
 * root would resolve against the extension host's cwd (ADR-024 § 7, "no caller
 * resolves against the process cwd"), so the helpers throw instead.
 *
 * @see Issue #2036
 */

import * as path from "path";

/** The per-clone data directory name inside the working tree today. */
const CLONE_DIR_NAME = ".nightgauge";

/**
 * Root-relative spellings of the four classes, POSIX-separated. For display
 * text (messages, remediation hints, setting defaults) and for callers that
 * join them onto a root themselves; `path.join` normalises the separator on
 * Windows. Prefer the functions below when a root is available.
 */
export const RELATIVE_PIPELINE_STATE_DIR = `${CLONE_DIR_NAME}/pipeline`;
export const RELATIVE_PLANS_DIR = `${CLONE_DIR_NAME}/plans`;
export const RELATIVE_RETROS_DIR = `${CLONE_DIR_NAME}/retros`;
export const RELATIVE_CLONE_LOGS_DIR = `${CLONE_DIR_NAME}/logs`;

/**
 * Throws unless `workspaceRoot` is a non-empty absolute path.
 */
function requireAbsoluteRoot(workspaceRoot: string, helper: string): string {
  if (typeof workspaceRoot !== "string" || workspaceRoot.trim() === "") {
    throw new Error(`${helper}: workspace root is empty; an absolute path is required`);
  }
  if (!path.isAbsolute(workspaceRoot)) {
    throw new Error(
      `${helper}: workspace root "${workspaceRoot}" is relative; an absolute path is required`
    );
  }
  return workspaceRoot;
}

/**
 * True when `workspaceRoot` is acceptable to the helpers below. Lets a caller
 * whose root may be unset keep its previous fallback instead of throwing.
 */
export function isUsableWorkspaceRoot(workspaceRoot: string | undefined | null): boolean {
  return (
    typeof workspaceRoot === "string" &&
    workspaceRoot.trim() !== "" &&
    path.isAbsolute(workspaceRoot)
  );
}

/** `<root>/.nightgauge/pipeline` — run state, contexts, history, traces. */
export function pipelineStateDir(workspaceRoot: string): string {
  return path.join(
    requireAbsoluteRoot(workspaceRoot, "pipelineStateDir"),
    RELATIVE_PIPELINE_STATE_DIR
  );
}

/** `<root>/.nightgauge/plans` — issue-keyed plans. */
export function plansDir(workspaceRoot: string): string {
  return path.join(requireAbsoluteRoot(workspaceRoot, "plansDir"), RELATIVE_PLANS_DIR);
}

/** `<root>/.nightgauge/retros` — issue-keyed retros. */
export function retrosDir(workspaceRoot: string): string {
  return path.join(requireAbsoluteRoot(workspaceRoot, "retrosDir"), RELATIVE_RETROS_DIR);
}

/** `<root>/.nightgauge/logs` — per-clone logs. */
export function cloneLogsDir(workspaceRoot: string): string {
  return path.join(requireAbsoluteRoot(workspaceRoot, "cloneLogsDir"), RELATIVE_CLONE_LOGS_DIR);
}
