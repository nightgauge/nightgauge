/**
 * Clone layout — the one place the extension addresses per-clone and
 * per-checkout data.
 *
 * Per-clone data lives in the clone's git directory, not the working tree
 * (ADR-024 § 7): `CLONE = <git-common-dir>/nightgauge`, with the four classes
 * `pipeline`, `plans`, `retros` and `logs` under it. For a normal clone that is
 * `<root>/.git/nightgauge/<class>`; from a linked worktree it is the main
 * clone's directory, shared by every checkout. Git never tracks its own
 * directory, so nothing here can be committed. Each checkout (the main one and
 * every linked worktree) also has its own `CHECKOUT = <git-dir>/nightgauge-worktree`
 * for its unkeyed singletons and runtime state ({@link checkoutPath}).
 *
 * The helpers stay synchronous for their many callers, over a per-root cache
 * in `@nightgauge/sdk` (`resolveCloneLayout`). Activation fills the cache for
 * every workspace folder ({@link primeCloneLayouts}), asking the binary
 * (`nightgauge layout --workdir <root>`) when one is installed and git
 * otherwise; a root nobody primed resolves on first use with one git call. A
 * cache hit never spawns anything.
 *
 * The workspace root must be a non-empty absolute path: a relative or empty
 * root would resolve against the extension host's cwd (ADR-024 § 7, "no caller
 * resolves against the process cwd"), so the helpers throw instead. Outside a
 * git repository they throw `NotAGitRepositoryError` ("not a git
 * repository"); nothing falls back to the working tree.
 *
 * @see Issue #2036, #2037
 */

import * as childProcess from "child_process";
import { promises as fsp, type Dirent } from "fs";
import * as path from "path";
import { promisify } from "util";
import {
  CHECKOUT_ENTRIES,
  clearCloneLayoutCache,
  cloneLayoutFor,
  cloneLayoutFromJson,
  primeCloneLayout,
  resolveCloneLayout,
  setCloneLayout,
  type CheckoutEntry,
  type CloneLayout,
} from "@nightgauge/sdk/dist/context/cloneLayout";

export {
  CHECKOUT_ENTRIES,
  clearCloneLayoutCache,
  cloneLayoutFor,
  setCloneLayout,
  type CheckoutEntry,
  type CloneLayout,
};

/**
 * Display spellings of the four classes for a normal clone, POSIX-separated,
 * matching the Go `layout.*Display()` helpers. For messages and remediation
 * hints only: never join one onto a root (from a linked worktree the real
 * directory is the main clone's); build paths with the functions below.
 */
export const PIPELINE_STATE_DISPLAY = ".git/nightgauge/pipeline";
export const PLANS_DISPLAY = ".git/nightgauge/plans";
export const RETROS_DISPLAY = ".git/nightgauge/retros";
export const CLONE_LOGS_DISPLAY = ".git/nightgauge/logs";

/**
 * Display spelling of the per-checkout root for the main checkout, matching Go
 * `layout.CheckoutDisplay()`. A linked worktree's is
 * `.git/worktrees/<name>/nightgauge-worktree`. For messages only.
 */
export const CHECKOUT_DISPLAY = ".git/nightgauge-worktree";

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

/** Throws unless `workspaceRoot` is a non-empty absolute path. */
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

/** The cached layout of an absolute root; throws outside a git repository. */
function layoutOf(workspaceRoot: string, helper: string): CloneLayout {
  return resolveCloneLayout(requireAbsoluteRoot(workspaceRoot, helper));
}

/**
 * True when `workspaceRoot` is acceptable to the helpers below: a non-empty
 * absolute path inside a git repository. Lets a caller whose root may be
 * unset, or not a repository, skip per-clone data instead of throwing. The
 * git answer is cached per root, so repeated checks never spawn git.
 */
export function isUsableWorkspaceRoot(
  workspaceRoot: string | undefined | null
): workspaceRoot is string {
  if (
    typeof workspaceRoot !== "string" ||
    workspaceRoot.trim() === "" ||
    !isAbsoluteRoot(workspaceRoot)
  ) {
    return false;
  }
  try {
    resolveCloneLayout(workspaceRoot);
    return true;
  } catch {
    return false;
  }
}

/** `<git-common-dir>/nightgauge/pipeline` — run state, contexts, history, traces. */
export function pipelineStateDir(workspaceRoot: string): string {
  return layoutOf(workspaceRoot, "pipelineStateDir").pipeline;
}

/** `<git-common-dir>/nightgauge/plans` — issue-keyed plans. */
export function plansDir(workspaceRoot: string): string {
  return layoutOf(workspaceRoot, "plansDir").plans;
}

/** `<git-common-dir>/nightgauge/retros` — issue-keyed retros. */
export function retrosDir(workspaceRoot: string): string {
  return layoutOf(workspaceRoot, "retrosDir").retros;
}

/** `<git-common-dir>/nightgauge/logs` — per-clone logs. */
export function cloneLogsDir(workspaceRoot: string): string {
  return layoutOf(workspaceRoot, "cloneLogsDir").logs;
}

/**
 * `<git-dir>/nightgauge-worktree` — this checkout's own run control and
 * runtime state (ADR-024 § 7). For a linked worktree it is inside that
 * worktree's git dir, not the main clone's.
 */
export function checkoutDir(workspaceRoot: string): string {
  return layoutOf(workspaceRoot, "checkoutDir").checkout;
}

/**
 * The path of a per-checkout entry for `workspaceRoot`:
 * `<git-dir>/nightgauge-worktree/<CHECKOUT_ENTRIES[entry]>`, optionally with
 * further path segments under a directory entry (`attention`, `health`, ...).
 */
export function checkoutPath(
  workspaceRoot: string,
  entry: CheckoutEntry,
  ...rest: string[]
): string {
  return path.join(
    layoutOf(workspaceRoot, "checkoutPath").checkout,
    CHECKOUT_ENTRIES[entry],
    ...rest
  );
}

/**
 * Absolute paths of the regular files directly in `dir` whose names satisfy
 * `match`; empty when `dir` does not exist. Use this, not
 * `vscode.workspace.findFiles`, to list a class directory: `findFiles` applies
 * `files.exclude` (which hides every `.git` directory by default) and searches
 * only the workspace folders, so it never finds files under the git directory.
 */
export async function listCloneFiles(
  dir: string,
  match: (name: string) => boolean
): Promise<string[]> {
  let entries: Dirent[];
  try {
    entries = await fsp.readdir(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  return entries.filter((e) => e.isFile() && match(e.name)).map((e) => path.join(dir, e.name));
}

/** Resolves the nightgauge binary; null when none is installed. */
export type LayoutBinaryResolver = () => Promise<string | null>;

let binaryResolver: LayoutBinaryResolver = async () => {
  // Imported lazily: BinaryResolver reads VS Code settings, and the pure
  // path helpers here must stay importable without a VS Code host.
  const { BinaryResolver } = await import("../services/BinaryResolver");
  return BinaryResolver.fromVSCode().resolve();
};

/** Replace how the binary is found (tests, or a host without VS Code). */
export function setCloneLayoutBinaryResolver(resolver: LayoutBinaryResolver): void {
  binaryResolver = resolver;
}

const LAYOUT_TIMEOUT_MS = 10_000;

/** `nightgauge layout --workdir <root>`, or undefined when it cannot answer. */
async function layoutFromBinary(root: string): Promise<CloneLayout | undefined> {
  let bin: string | null;
  try {
    bin = await binaryResolver();
  } catch {
    return undefined;
  }
  if (!bin) return undefined;
  try {
    // Promisified per call, not at import: suites that mock child_process
    // partially still import the pure helpers here.
    const execFileAsync = promisify(childProcess.execFile);
    const { stdout } = await execFileAsync(bin, ["layout", "--workdir", root], {
      timeout: LAYOUT_TIMEOUT_MS,
      windowsHide: true,
    });
    const layout = cloneLayoutFromJson(stdout);
    const classes = [layout.pipeline, layout.plans, layout.retros, layout.logs, layout.checkout];
    return classes.every((p) => path.isAbsolute(p)) ? { ...layout, root } : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Fills the layout cache for each absolute root without blocking the host:
 * the binary's answer when it gives one, else git's (the two are pinned by a
 * parity test). A root that is not in a git repository is remembered as such.
 * Never rejects.
 */
export async function primeCloneLayouts(roots: readonly string[]): Promise<void> {
  const unique = [...new Set(roots.filter((r) => typeof r === "string" && isAbsoluteRoot(r)))];
  await Promise.all(
    unique.map(async (root) => {
      const fromBinary = await layoutFromBinary(root);
      if (fromBinary) {
        setCloneLayout(root, fromBinary);
        return;
      }
      await primeCloneLayout(root).catch(() => undefined);
    })
  );
}

/**
 * Forgets `removed` roots and primes `added` ones — the workspace-folder
 * change handler.
 */
export async function refreshCloneLayouts(
  added: readonly string[],
  removed: readonly string[]
): Promise<void> {
  for (const root of removed) clearCloneLayoutCache(root);
  for (const root of added) clearCloneLayoutCache(root);
  await primeCloneLayouts(added);
}
