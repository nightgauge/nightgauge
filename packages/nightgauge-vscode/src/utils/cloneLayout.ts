/**
 * Clone layout — the one place the extension addresses per-clone data.
 *
 * Mirrors the Go class resolvers of ADR-024 § 1 and § 7 (`PipelineStateDir`,
 * `PlansDir`, `RetrosDir`, `CloneLogsDir`). Every caller in the extension
 * builds a per-clone path through these helpers, so the later move of the
 * location out of the working tree (#2037) edits this module alone.
 *
 * Today the classes live in the working tree, at `<root>/.nightgauge/<class>`.
 * The helpers return that, joined with `path.join`: every caller resolves the
 * same file it did before, with native separators. (A few callers used to
 * build the path with a template string, which on Windows mixed `\\` and `/`;
 * the string differs there, the file does not.)
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
 * True when `root` is absolute under `pathImpl` (default: the host's `path`).
 * On Windows a rooted path without a drive or UNC prefix (`\\x`) is
 * drive-relative — it resolves against the current drive — so it is refused.
 * `pathImpl` is injectable so the Windows rule is testable on any host.
 */
export function isAbsoluteRoot(
  root: string,
  pathImpl: Pick<typeof path, "isAbsolute" | "sep"> = path
): boolean {
  if (!pathImpl.isAbsolute(root)) return false;
  if (pathImpl.sep === "\\") {
    return /^[A-Za-z]:[\\/]/.test(root) || /^[\\/]{2}[^\\/]+[\\/]+[^\\/]+/.test(root);
  }
  return true;
}

/**
 * Throws unless `workspaceRoot` is a non-empty absolute path.
 */
function requireAbsoluteRoot(workspaceRoot: string, helper: string): string {
  if (typeof workspaceRoot !== "string" || workspaceRoot.trim() === "") {
    throw new Error(`${helper}: workspace root is empty; an absolute path is required`);
  }
  if (!isAbsoluteRoot(workspaceRoot)) {
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
export function isUsableWorkspaceRoot(
  workspaceRoot: string | undefined | null
): workspaceRoot is string {
  return (
    typeof workspaceRoot === "string" &&
    workspaceRoot.trim() !== "" &&
    isAbsoluteRoot(workspaceRoot)
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

/** Normalises a root-relative setting value for comparison with a default. */
function normaliseRelative(value: string): string {
  return value
    .replace(/\\/g, "/")
    .replace(/^(\.\/)+/, "")
    .replace(/\/+$/, "");
}

/**
 * Resolves a user-configurable per-clone directory setting (for example
 * `core.context_path` or `pipeline.logs.dir`).
 *
 * - Unset, empty, or equal to its default (`defaultRel`, compared after
 *   normalising separators, a leading `./` and a trailing `/`): resolves
 *   through `resolver`, so the location follows this module when it moves.
 * - Any other value is a user override and is joined onto the root with
 *   `join` (default `path.join`), exactly as the caller did before.
 *
 * The root is validated either way: an unusable root throws rather than
 * resolving against the host's cwd.
 */
export function resolveCloneSetting(
  workspaceRoot: string,
  value: string | undefined | null,
  defaultRel: string,
  resolver: (workspaceRoot: string) => string,
  join: (root: string, rel: string) => string = path.join
): string {
  if (
    value === undefined ||
    value === null ||
    value.trim() === "" ||
    normaliseRelative(value) === normaliseRelative(defaultRel)
  ) {
    return resolver(workspaceRoot);
  }
  return join(requireAbsoluteRoot(workspaceRoot, "resolveCloneSetting"), value);
}
