/**
 * Put a copy of the demo workspace's per-clone and per-checkout data where the
 * extension reads it (ADR-024 § 7, #2037).
 *
 * Per-clone data (`pipeline`, `plans`, `retros`, `logs`) lives in the clone's
 * git directory, per-checkout data (`health/`, `attention/`, `focus.yaml`, ...)
 * in the checkout's own git directory, and nothing under `.git/` can be
 * committed. So the committed demo workspace (`demo/workspace/`) keeps that
 * data under `.nightgauge/<class>/` and `.nightgauge/<entry>`. Those paths are
 * a staging area for this helper and nothing else: the extension never reads
 * either kind of data from the working tree. Every consumer that materializes
 * the demo workspace (the `npm run demo` session and the VS Code host tier)
 * calls {@link placeCloneData} on its copy.
 */

import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import {
  CHECKOUT_ENTRIES,
  checkoutPath,
  clearCloneLayoutCache,
  cloneLogsDir,
  pipelineStateDir,
  plansDir,
  retrosDir,
  type CheckoutEntry,
} from "../src/utils/cloneLayout";

/**
 * Each per-clone class: its staging directory in a demo tree, relative to the
 * tree's root, and the resolver that addresses it in a clone.
 */
export const STAGED_CLONE_CLASSES: ReadonlyArray<readonly [string, (root: string) => string]> = [
  [path.join(".nightgauge", "pipeline"), pipelineStateDir],
  [path.join(".nightgauge", "plans"), plansDir],
  [path.join(".nightgauge", "retros"), retrosDir],
  [path.join(".nightgauge", "logs"), cloneLogsDir],
];

/**
 * The run-control singletons are staged inside the class directory they sat in
 * before ADR-024 § 7 gave each checkout its own; every other entry directly
 * under `.nightgauge/`.
 */
const STAGED_IN_CLASS: Partial<Record<CheckoutEntry, string>> = {
  currentRun: "pipeline",
  runState: "pipeline",
  batchState: "pipeline",
  queueState: "pipeline",
  plan: "pipeline",
  backendLog: "logs",
};

/**
 * Each per-checkout entry: its staging path in a demo tree (a directory or a
 * file), and the resolver that addresses it in the checkout's own directory.
 * Placed before the classes, so a singleton staged in `.nightgauge/pipeline/`
 * reaches the checkout rather than the shared pipeline directory.
 */
export const STAGED_CHECKOUT_ENTRIES: ReadonlyArray<readonly [string, (root: string) => string]> = (
  Object.keys(CHECKOUT_ENTRIES) as CheckoutEntry[]
).map(
  (entry) =>
    [
      path.join(".nightgauge", STAGED_IN_CLASS[entry] ?? "", CHECKOUT_ENTRIES[entry]),
      (root: string) => checkoutPath(root, entry),
    ] as const
);

/** Variables that would point git at some other repository. */
const GIT_LOCATION_ENV = [
  "GIT_DIR",
  "GIT_WORK_TREE",
  "GIT_COMMON_DIR",
  "GIT_INDEX_FILE",
  "GIT_OBJECT_DIRECTORY",
];

/**
 * Make `target` a git repository (`git init`, no commit) and move each
 * per-clone class staged in `tree` into `target`'s clone directory, and each
 * per-checkout entry into `target`'s checkout directory, merging into
 * whatever is there already. `tree` is a copy of the demo workspace;
 * `target` defaults to it. Everything else in `tree` stays where it is: the
 * caller copies it into the working tree (for a separate `target`) or
 * already has it there.
 */
export function placeCloneData(tree: string, target: string = tree): void {
  const env = { ...process.env };
  for (const k of GIT_LOCATION_ENV) delete env[k];
  execFileSync("git", ["init", "-q"], { cwd: target, env, stdio: "pipe" });
  // A lookup made before `git init` cached "not a git repository".
  clearCloneLayoutCache(target);
  for (const [staged, resolve] of [...STAGED_CHECKOUT_ENTRIES, ...STAGED_CLONE_CLASSES]) {
    const from = path.join(tree, staged);
    if (!fs.existsSync(from)) continue;
    const to = resolve(target);
    fs.mkdirSync(fs.statSync(from).isDirectory() ? to : path.dirname(to), { recursive: true });
    fs.cpSync(from, to, { recursive: true, force: true });
    fs.rmSync(from, { recursive: true, force: true });
  }
}
