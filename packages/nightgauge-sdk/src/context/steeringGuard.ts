/**
 * steeringGuard — keeps the ephemeral Codex steering block out of commits.
 *
 * The managed block is written into the working tree's AGENTS.md so a Codex
 * stage can read it. Stripping it from the working tree after the stage is not
 * enough: the stage's own agent commits WHILE the block is present
 * (`git add -A`, `git commit -a`), and every pipeline-owned commit that runs
 * `git add -A` sweeps it in too. Two deterministic guards close that:
 *
 *  - {@link sanitizeStagedAgentsMdSync} runs between staging and committing at
 *    every pipeline-owned commit site and rewrites the STAGED AGENTS.md without
 *    the block, leaving the working tree (and a live stage's steering) alone.
 *  - {@link repairCommittedSteering} runs after a stage: when HEAD's AGENTS.md
 *    carries the block it adds one commit removing exactly the block, so the
 *    branch tip never carries generated steering.
 *
 * Mirrors the Go `internal/execution/codexprovision/guard.go` (issue 1675).
 */

import { execFile, execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { promisify } from "node:util";
import { CODEX_MANAGED_BEGIN, CODEX_MANAGED_END, stripManagedBlock } from "./steeringSources.js";

const execFileAsync = promisify(execFile);

/** Subject of the commit {@link repairCommittedSteering} adds. */
export const STEERING_REPAIR_COMMIT_MESSAGE =
  "chore(agents): remove generated Nightgauge steering from AGENTS.md";

const REPAIR_COMMIT_BODY =
  "The pipeline writes this block for Codex stages and removes it afterwards; it was committed while present.";

const GIT_TIMEOUT_MS = 30_000;
const PUSH_TIMEOUT_MS = 120_000;

/** Either marker alone is a leak: a half-block is still generated content. */
export function containsManagedSteering(content: string): boolean {
  return content.includes(CODEX_MANAGED_BEGIN) || content.includes(CODEX_MANAGED_END);
}

/** Remove the managed block (and any orphaned marker line), keeping user content. */
export function stripManagedSteering(content: string): string {
  const out = stripManagedBlock(content);
  if (!containsManagedSteering(out)) return out;
  return out
    .split("\n")
    .filter((l) => {
      const t = l.trim();
      return t !== CODEX_MANAGED_BEGIN && t !== CODEX_MANAGED_END;
    })
    .join("\n");
}

/** What {@link repairCommittedSteering} did. */
export interface SteeringRepairResult {
  repaired: boolean;
  oldHead?: string;
  newHead?: string;
  pushed: boolean;
  pushError?: string;
}

function git(
  cwd: string,
  args: string[],
  opts: { env?: NodeJS.ProcessEnv; input?: string } = {}
): string {
  return execFileSync("git", ["-C", cwd, ...args], {
    encoding: "utf-8",
    env: opts.env ?? process.env,
    input: opts.input,
    timeout: GIT_TIMEOUT_MS,
    stdio: ["pipe", "pipe", "pipe"],
  });
}

function tryGit(
  cwd: string,
  args: string[],
  opts: { env?: NodeJS.ProcessEnv; input?: string } = {}
): string | null {
  try {
    return git(cwd, args, opts);
  } catch {
    return null;
  }
}

function agentsPathIn(dir: string): { top: string; rel: string } | null {
  const top = tryGit(dir, ["rev-parse", "--show-toplevel"]);
  const prefix = tryGit(dir, ["rev-parse", "--show-prefix"]);
  if (top === null || prefix === null) return null;
  return { top: top.trim(), rel: path.posix.join(prefix.trim(), "AGENTS.md") };
}

function blobMode(listing: string | null): string {
  const f = (listing ?? "").trim().split(/\s+/);
  return f[0] === "100644" || f[0] === "100755" ? f[0] : "100644";
}

function stageStripped(
  top: string,
  rel: string,
  stripped: string,
  mode: string,
  env?: NodeJS.ProcessEnv
): void {
  if (stripped.trim() === "") {
    git(top, ["update-index", "--force-remove", "--", rel], { env });
    return;
  }
  const blob = git(top, ["hash-object", "-w", "--stdin"], { input: stripped }).trim();
  git(top, ["update-index", "--add", "--cacheinfo", `${mode},${blob},${rel}`], { env });
}

/**
 * Rewrite the staged AGENTS.md without the managed steering block. Call after
 * staging and before `git commit` at every pipeline-owned commit site. The
 * working tree is untouched. Returns true when the index changed; a directory
 * git cannot read, or an unstaged AGENTS.md, is a no-op.
 */
export function sanitizeStagedAgentsMdSync(dir: string): boolean {
  const loc = agentsPathIn(dir);
  if (!loc) return false;
  const staged = tryGit(loc.top, ["show", `:${loc.rel}`]);
  if (staged === null || !containsManagedSteering(staged)) return false;
  const listing = tryGit(loc.top, ["ls-files", "-s", "--", loc.rel]);
  let stripped = stripManagedSteering(staged);
  if (stripped.trim() === "") {
    // Nothing but generated steering is staged: keep the committed version
    // (itself cleaned) rather than deleting a file HEAD carries.
    const head = tryGit(loc.top, ["show", `HEAD:${loc.rel}`]);
    if (head !== null) stripped = stripManagedSteering(head);
  }
  stageStripped(loc.top, loc.rel, stripped, blobMode(listing));
  return true;
}

/**
 * Runs `git -C cwd ...args`, resolving stdout or null on any failure. Callers
 * that own their process layer (the VS Code extension host) pass their own so
 * every git call goes through the same seam as the rest of their subprocesses.
 */
export type SteeringGitRunner = (cwd: string, args: string[]) => Promise<string | null>;

async function gitAsync(cwd: string, args: string[]): Promise<string | null> {
  try {
    const { stdout } = await execFileAsync("git", ["-C", cwd, ...args], {
      encoding: "utf-8",
      timeout: GIT_TIMEOUT_MS,
    });
    return String(stdout);
  } catch {
    return null;
  }
}

/**
 * Asynchronous {@link sanitizeStagedAgentsMdSync}, for callers on an event loop
 * that must never block on a subprocess (the VS Code extension host).
 * Throws when the index cannot be rewritten, so the caller refuses to commit.
 */
export async function sanitizeStagedAgentsMd(dir: string): Promise<boolean> {
  const top = (await gitAsync(dir, ["rev-parse", "--show-toplevel"]))?.trim();
  const prefix = await gitAsync(dir, ["rev-parse", "--show-prefix"]);
  if (!top || prefix === null) return false;
  const rel = path.posix.join(prefix.trim(), "AGENTS.md");
  const staged = await gitAsync(top, ["show", `:${rel}`]);
  if (staged === null || !containsManagedSteering(staged)) return false;
  const mode = blobMode(await gitAsync(top, ["ls-files", "-s", "--", rel]));
  let stripped = stripManagedSteering(staged);
  if (stripped.trim() === "") {
    const head = await gitAsync(top, ["show", `HEAD:${rel}`]);
    if (head !== null) stripped = stripManagedSteering(head);
  }
  if (stripped.trim() === "") {
    if ((await gitAsync(top, ["update-index", "--force-remove", "--", rel])) === null) {
      throw new Error("could not unstage generated AGENTS.md steering");
    }
    return true;
  }
  // Through a temporary file rather than stdin, so the whole async path uses
  // the promisified execFile and no call can wait on a callback forever.
  const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "nightgauge-steering-"));
  let blob: string | undefined;
  try {
    const tmpFile = path.join(tmpDir, "AGENTS.md");
    await fs.promises.writeFile(tmpFile, stripped, "utf-8");
    blob = (await gitAsync(top, ["hash-object", "-w", "--no-filters", tmpFile]))?.trim();
  } finally {
    await fs.promises.rm(tmpDir, { recursive: true, force: true });
  }
  if (
    !blob ||
    (await gitAsync(top, ["update-index", "--add", "--cacheinfo", `${mode},${blob},${rel}`])) ===
      null
  ) {
    throw new Error("could not stage AGENTS.md without generated steering");
  }
  return true;
}

/**
 * Add one commit removing the managed steering block when HEAD's AGENTS.md
 * carries it — built with plumbing on a temporary index, so no hooks run and
 * the working tree is untouched; the real index is then brought in line.
 * Synchronous and local only; see {@link repairCommittedSteering} for the
 * variant that also publishes the repair.
 */
export function repairCommittedSteeringSync(dir: string): SteeringRepairResult {
  const res: SteeringRepairResult = { repaired: false, pushed: false };
  const loc = agentsPathIn(dir);
  if (!loc) return res;
  const committed = tryGit(loc.top, ["show", `HEAD:${loc.rel}`]);
  if (committed === null || !containsManagedSteering(committed)) return res;
  const oldHead = git(loc.top, ["rev-parse", "HEAD"]).trim();
  res.oldHead = oldHead;
  const listing = tryGit(loc.top, ["ls-tree", "HEAD", "--", loc.rel]);

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-steering-"));
  const env = { ...process.env, GIT_INDEX_FILE: path.join(tmpDir, "index") };
  try {
    git(loc.top, ["read-tree", "HEAD"], { env });
    stageStripped(loc.top, loc.rel, stripManagedSteering(committed), blobMode(listing), env);
    const tree = git(loc.top, ["write-tree"], { env }).trim();
    const newHead = git(loc.top, [
      "commit-tree",
      tree,
      "-p",
      oldHead,
      "-m",
      STEERING_REPAIR_COMMIT_MESSAGE,
      "-m",
      REPAIR_COMMIT_BODY,
    ]).trim();
    git(loc.top, ["update-ref", "-m", STEERING_REPAIR_COMMIT_MESSAGE, "HEAD", newHead, oldHead]);
    res.repaired = true;
    res.newHead = newHead;
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
  // The real index still holds the leaked blob; point it at the repaired one.
  sanitizeStagedAgentsMdSync(loc.top);
  return res;
}

/** Every AGENTS.md tracked at `rev` that carries the managed steering markers. */
export async function committedSteeringAt(
  dir: string,
  rev: string,
  run: SteeringGitRunner = gitAsync
): Promise<string[] | null> {
  const list = await run(dir, ["ls-tree", "-r", "--name-only", "-z", rev]);
  if (list === null) return null;
  const leaked: string[] = [];
  for (const p of list.split("\0")) {
    if (p !== "AGENTS.md" && !p.endsWith("/AGENTS.md")) continue;
    const content = await run(dir, ["show", `${rev}:${p}`]);
    if (content === null) return null;
    if (containsManagedSteering(content)) leaked.push(p);
  }
  return leaked;
}

/**
 * Push `dir`'s HEAD to `remote:ref` when that is exactly "publish the steering
 * repair": `remoteTip` is an ancestor of HEAD, every commit between them is a
 * repair commit, and HEAD carries no managed block. Covers a repair made just
 * now and a local HEAD repaired earlier whose upstream tip is still leaked;
 * never publishes other unpushed work. Mirrors the Go PublishSteeringRepair.
 */
export async function publishSteeringRepair(
  dir: string,
  remote: string,
  ref: string,
  remoteTip: string,
  run: SteeringGitRunner = gitAsync
): Promise<{ pushed: boolean; pushError?: string }> {
  if (!remote || !ref || !remoteTip) return { pushed: false };
  if ((await run(dir, ["merge-base", "--is-ancestor", remoteTip, "HEAD"])) === null) {
    return { pushed: false };
  }
  const subjects = (await run(dir, ["log", "--format=%s", `${remoteTip}..HEAD`]))?.trim();
  if (!subjects || subjects.split("\n").some((l) => l.trim() !== STEERING_REPAIR_COMMIT_MESSAGE)) {
    return { pushed: false };
  }
  const leaked = await committedSteeringAt(dir, "HEAD", run);
  if (leaked === null || leaked.length > 0) return { pushed: false };
  if ((await run(dir, ["push", remote, `HEAD:${ref}`])) === null) {
    return { pushed: false, pushError: `git push ${remote} HEAD:${ref} failed` };
  }
  return { pushed: true };
}

async function branchUpstream(
  dir: string,
  run: SteeringGitRunner = gitAsync
): Promise<{ branch: string; remote: string; merge: string } | null> {
  const branch = (await run(dir, ["symbolic-ref", "--short", "-q", "HEAD"]))?.trim();
  if (!branch) return null;
  const remote = (await run(dir, ["config", "--get", `branch.${branch}.remote`]))?.trim();
  const merge = (await run(dir, ["config", "--get", `branch.${branch}.merge`]))?.trim();
  if (!remote || !merge) return null;
  return { branch, remote, merge };
}

/**
 * {@link repairCommittedSteeringSync}, then — when `push` is set and the
 * upstream tip is the leaked history plus nothing but repair commits (an open
 * pull request carries the block) — publish the repair there. Also publishes
 * a local repair made earlier that never reached the upstream. A push failure
 * is reported in the result: the repair commit exists and the next pipeline
 * push carries it.
 */
export async function repairCommittedSteering(
  dir: string,
  opts: { push?: boolean } = {}
): Promise<SteeringRepairResult> {
  const res = repairCommittedSteeringSync(dir);
  if (!opts.push) return res;
  const loc = agentsPathIn(dir);
  if (!loc) return res;
  const up = await branchUpstream(loc.top);
  const upstream = (
    await gitAsync(loc.top, ["rev-parse", "-q", "--verify", "@{upstream}"])
  )?.trim();
  if (!up || !upstream) return res;
  const pub = await publishSteeringRepair(loc.top, up.remote, up.merge, upstream);
  res.pushed = pub.pushed;
  if (pub.pushError) res.pushError = pub.pushError;
  return res;
}

/** What {@link guardUpstreamHead} found on the branch's published head. */
export interface UpstreamSteeringVerdict {
  /** The upstream tip inspected, when one could be resolved. */
  headSha?: string;
  /** AGENTS.md paths at that tip carrying the managed block. */
  leaked: string[];
  /** The block was repaired locally and the repair pushed. */
  pushed: boolean;
  pushError?: string;
  /** False when the upstream could not be fetched or read. */
  inspected: boolean;
}

/**
 * The TypeScript twin of the pr-merge steering gate, for a merge that runs
 * without the Go runner (issue 1675): fetch the current branch's upstream,
 * report AGENTS.md files at its tip that carry the managed block, and when
 * any do, repair locally and publish the repair. The caller refuses the merge
 * whenever `leaked` is non-empty — a push changes the head, so required checks
 * must re-run first.
 */
export async function guardUpstreamHead(
  dir: string,
  run: SteeringGitRunner = gitAsync
): Promise<UpstreamSteeringVerdict> {
  const verdict: UpstreamSteeringVerdict = { leaked: [], pushed: false, inspected: false };
  const up = await branchUpstream(dir, run);
  if (!up) return verdict;
  const branchRef = up.merge.replace(/^refs\/heads\//, "");
  const tracking = `refs/remotes/${up.remote}/${branchRef}`;
  if ((await run(dir, ["fetch", "-q", up.remote, `+${up.merge}:${tracking}`])) === null) {
    return verdict;
  }
  const tip = (await run(dir, ["rev-parse", tracking]))?.trim();
  if (!tip) return verdict;
  verdict.headSha = tip;
  const leaked = await committedSteeringAt(dir, tip, run);
  if (leaked === null) return verdict;
  verdict.inspected = true;
  verdict.leaked = leaked;
  if (leaked.length === 0) return verdict;
  try {
    repairCommittedSteeringSync(dir);
  } catch (err) {
    verdict.pushError = err instanceof Error ? err.message : String(err);
    return verdict;
  }
  const pub = await publishSteeringRepair(dir, up.remote, up.merge, tip, run);
  verdict.pushed = pub.pushed;
  verdict.pushError =
    pub.pushError ??
    (pub.pushed
      ? undefined
      : "the local branch is not the published head plus repair commits only");
  return verdict;
}
