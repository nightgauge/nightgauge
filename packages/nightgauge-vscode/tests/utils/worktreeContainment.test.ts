/**
 * worktreeContainment.test.ts
 *
 * Issue #129: a stage must not write outside its own worktree, and when it
 * does, the pipeline must say so accurately and preserve the work.
 *
 * The incident: `feature-dev` ran for an issue whose code lived elsewhere,
 * produced ZERO changes in its own worktree, and left 5 modified + 2 new files
 * in one sibling repo and 1 modified + 3 new files in another that was checked
 * out on `main`. `feature-validate` then reported "no implementation work
 * detected" — true of the branch, and completely misleading. The run was billed
 * in full and recorded as a failure with substantial work stranded uncommitted
 * in a repo the operator was actively using.
 *
 * These tests run against REAL git repositories in a temp directory. The whole
 * behaviour under test is "what does git report, and what is on disk
 * afterwards" — a mocked git would prove nothing about either. In particular
 * the non-destructiveness assertions (nothing staged, committed, reverted or
 * even touched in a sibling repo) are only meaningful against real git.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  captureContainmentBaseline,
  detectContainmentBreach,
  formatContainmentFailure,
  formatContainmentWarning,
  parsePorcelainZ,
  resolveContainmentTargets,
  CONTAINMENT_DIR,
  CONTAINMENT_ERROR_MARKER,
} from "../../src/utils/worktreeContainment";

let tmp: string;
/** The repo the stage belongs to. Its `.worktrees/issue-129` is the stage CWD. */
let primary: string;
/** The stage's worktree — the only place it is allowed to write. */
let worktree: string;
/** A second workspace repo the stage has no business touching. */
let sibling: string;

function git(args: string[], cwd: string): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

function initRepo(dir: string, branch: string): void {
  fs.mkdirSync(dir, { recursive: true });
  git(["init", "--quiet"], dir);
  git(["symbolic-ref", "HEAD", `refs/heads/${branch}`], dir);
  git(["config", "user.email", "pipeline@nightgauge.test"], dir);
  git(["config", "user.name", "Nightgauge Test"], dir);
  git(["config", "commit.gpgsign", "false"], dir);
  fs.mkdirSync(path.join(dir, "src"), { recursive: true });
  fs.writeFileSync(path.join(dir, "README.md"), "base\n");
  fs.writeFileSync(path.join(dir, "src", "router.ts"), "export const routes = [];\n");
  fs.writeFileSync(path.join(dir, "src", "handlers.ts"), "export function handle() {}\n");
  git(["add", "-A"], dir);
  git(["commit", "--quiet", "-m", "base"], dir);
}

/** Full dirty state + HEAD + branch, for "nothing was touched" assertions. */
function repoState(dir: string): { status: string; head: string; branch: string; log: string } {
  return {
    status: git(["status", "--porcelain", "--untracked-files=all"], dir),
    head: git(["rev-parse", "HEAD"], dir).trim(),
    branch: git(["rev-parse", "--abbrev-ref", "HEAD"], dir).trim(),
    log: git(["log", "--oneline"], dir),
  };
}

beforeEach(() => {
  // realpath: on macOS `os.tmpdir()` is `/var/...`, a symlink to `/private/var/...`.
  // git reports the resolved form, so the test's own paths must be resolved too
  // or every path comparison below is comparing two spellings of one directory.
  tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-containment-")));
  primary = path.join(tmp, "primary");
  sibling = path.join(tmp, "sibling");
  initRepo(primary, "main");
  initRepo(sibling, "main");
  // A real linked worktree, so the stage CWD is genuinely a worktree of
  // `primary` rather than a lookalike directory.
  worktree = path.join(primary, ".worktrees", "issue-129");
  git(["worktree", "add", "--quiet", "-b", "fix/129", worktree, "HEAD"], primary);
});

afterEach(() => {
  fs.rmSync(tmp, { recursive: true, force: true });
});

/** What the incident produced in a sibling repo: modified files plus new ones. */
function stageWritesIntoSibling(): void {
  fs.writeFileSync(
    path.join(sibling, "src", "handlers.ts"),
    "export function handle() { /* impl */ }\n"
  );
  fs.writeFileSync(
    path.join(sibling, "src", "router.ts"),
    "export const routes = ['/v1/things'];\n"
  );
  fs.mkdirSync(path.join(sibling, "src", "api"), { recursive: true });
  fs.writeFileSync(path.join(sibling, "src", "api", "things.ts"), "export const things = 1;\n");
}

describe("resolveContainmentTargets — what counts as out of bounds (#129)", () => {
  it("excludes the stage's own directory and keeps every sibling", async () => {
    const targets = await resolveContainmentTargets(worktree, [primary, sibling, worktree]);
    expect(targets).toContain(sibling);
    expect(targets).not.toContain(worktree);
  });

  it("keeps the repo's MAIN checkout in scope when the stage runs in a linked worktree", async () => {
    // The incident's second repo was a main checkout sitting on `main`.
    // A worktree's parent repo is a DIFFERENT working tree, so it stays in
    // scope even though it contains the stage CWD.
    const targets = await resolveContainmentTargets(worktree, [primary, sibling]);
    expect(targets).toContain(primary);
  });

  it("excludes the repo itself when the stage runs directly in it (single-repo mode)", async () => {
    // No worktree: `primary` IS the stage's tree, so every write to it is legitimate.
    const targets = await resolveContainmentTargets(primary, [primary, sibling]);
    expect(targets).toEqual([sibling]);
  });
});

describe("(a) stage writes into a sibling repo (#129)", () => {
  it("detects the breach and names the repo and every path", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toHaveLength(1);
    const breach = report.breaches[0];
    expect(breach.repoName).toBe("sibling");
    expect(breach.repoPath).toBe(sibling);
    expect(breach.paths).toEqual(["src/api/things.ts", "src/handlers.ts", "src/router.ts"]);
    expect(breach.ambiguousPaths).toEqual([]);
  });

  it("reports a reason that names the repo and paths, not 'no implementation work detected'", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const reason = formatContainmentFailure("feature-dev", report);
    expect(reason).toContain(CONTAINMENT_ERROR_MARKER);
    expect(reason).toContain("wrote outside its worktree");
    expect(reason).toContain("sibling");
    expect(reason).toContain(sibling);
    expect(reason).toContain("src/handlers.ts");
    expect(reason).toContain("src/api/things.ts");
    // The whole point: triage must not be sent downstream.
    expect(reason).not.toContain("no implementation work detected");
  });

  it("preserves the changes as an applicable patch outside the repo it came from", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const patchPath = report.breaches[0].patchPath;
    expect(patchPath).toBeDefined();
    expect(fs.existsSync(patchPath!)).toBe(true);

    // Written under the stage repo's CANONICAL root, which survives the
    // worktree teardown a re-dispatch performs — not inside the worktree, and
    // not inside the repo that was written to.
    expect(patchPath!.startsWith(path.join(primary, CONTAINMENT_DIR))).toBe(true);
    expect(patchPath!.startsWith(sibling)).toBe(false);
    expect(patchPath!.startsWith(worktree)).toBe(false);

    const patch = fs.readFileSync(patchPath!, "utf-8");
    expect(patch).toContain("src/handlers.ts");
    expect(patch).toContain("/* impl */");
    expect(patch).toContain("'/v1/things'");
    // Untracked files must be in the patch too — they are pure new work with
    // no HEAD version to recover from.
    expect(patch).toContain("export const things = 1;");

    // And it is genuinely recoverable: a clean checkout at the same base takes it.
    const recovery = path.join(tmp, "recovery");
    git(["clone", "--quiet", sibling, recovery], tmp);
    execFileSync("git", ["apply", patchPath!], { cwd: recovery });
    expect(fs.readFileSync(path.join(recovery, "src", "handlers.ts"), "utf-8")).toContain(
      "/* impl */"
    );
    expect(fs.readFileSync(path.join(recovery, "src", "api", "things.ts"), "utf-8")).toContain(
      "export const things = 1;"
    );
  });

  it("writes a manifest naming the repo, the paths and the recovery command", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const manifest = JSON.parse(
      fs.readFileSync(path.join(report.artifactDir!, "manifest.json"), "utf-8")
    );
    expect(manifest.stage).toBe("feature-dev");
    expect(manifest.issueNumber).toBe(129);
    expect(manifest.breaches[0].repoPath).toBe(sibling);
    expect(manifest.breaches[0].paths).toContain("src/handlers.ts");
    expect(manifest.note).toContain("apply");
    expect(manifest.note).toContain("<repoName>.patch");
  });

  it("does not modify, stage, commit or revert ANYTHING in the repo it inspected", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const before = repoState(sibling);

    await detectContainmentBreach({ baseline, stage: "feature-dev", issueNumber: 129 });

    // Identical HEAD, branch, commit log and dirty state: no add, no commit,
    // no stash, no checkout. The files are exactly where the stage left them.
    expect(repoState(sibling)).toEqual(before);
    expect(fs.readFileSync(path.join(sibling, "src", "handlers.ts"), "utf-8")).toBe(
      "export function handle() { /* impl */ }\n"
    );
    expect(fs.existsSync(path.join(sibling, "src", "api", "things.ts"))).toBe(true);
    // Nothing was written INTO the sibling either.
    expect(fs.existsSync(path.join(sibling, ".nightgauge"))).toBe(false);
  });

  it("catches a write into the repo's own main checkout while the stage runs in a worktree", async () => {
    // The incident's second repo: same repository as the stage, but the main
    // checkout, sitting on `main`. A `git pull` there destroys the work.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(path.join(primary, "src", "router.ts"), "export const routes = ['leaked'];\n");

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches.map((b) => b.repoName)).toEqual(["primary"]);
    expect(report.breaches[0].paths).toEqual(["src/router.ts"]);
  });
});

describe("(b) pre-existing operator work is never attributed to the stage (#129)", () => {
  it("ignores a sibling that was already dirty and that the stage never touched", async () => {
    // The operator has been working in this repo for an hour.
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work in progress\n");
    fs.writeFileSync(path.join(sibling, "operator-notes.md"), "scratch\n");
    const before = repoState(sibling);

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    // Stage does its job inside its own worktree.
    fs.writeFileSync(
      path.join(worktree, "src", "handlers.ts"),
      "export function handle() { /* ok */ }\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(report.artifactDir).toBeUndefined();
    // Untouched, byte for byte.
    expect(repoState(sibling)).toEqual(before);
    expect(fs.readFileSync(path.join(sibling, "src", "router.ts"), "utf-8")).toBe(
      "// operator work in progress\n"
    );
    expect(fs.existsSync(path.join(primary, CONTAINMENT_DIR))).toBe(false);
  });

  it("attributes only the stage's new paths when the operator is dirty in the same repo", async () => {
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work in progress\n");
    fs.writeFileSync(path.join(sibling, "operator-notes.md"), "scratch\n");

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* impl */ }\n"
    );

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toEqual(["src/handlers.ts"]);
    expect(report.breaches[0].paths).not.toContain("src/router.ts");
    expect(report.breaches[0].paths).not.toContain("operator-notes.md");

    // The operator's content must not leak into the captured patch either.
    const patch = fs.readFileSync(report.breaches[0].patchPath!, "utf-8");
    expect(patch).toContain("/* impl */");
    expect(patch).not.toContain("operator work in progress");
    expect(patch).not.toContain("scratch");
  });

  it("warns but does NOT fail when an already-dirty path changes during the stage", async () => {
    // Indistinguishable from the filesystem: the operator saving their own
    // in-progress file, or the stage editing a file the operator had dirtied.
    // The operator is the likelier explanation and the delta is an inseparable
    // mix, so this is reported and left alone — never a failure, never captured.
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work, revision 1\n");

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(sibling, "src", "router.ts"),
      "// operator work, revision 2 (longer)\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toHaveLength(1);
    expect(report.warnings[0].ambiguousPaths).toEqual(["src/router.ts"]);
    expect(report.artifactDir).toBeUndefined();
    expect(fs.existsSync(path.join(primary, CONTAINMENT_DIR))).toBe(false);
    expect(formatContainmentWarning("feature-dev", report)).toContain("src/router.ts");
    expect(fs.readFileSync(path.join(sibling, "src", "router.ts"), "utf-8")).toBe(
      "// operator work, revision 2 (longer)\n"
    );
  });
});

describe("(c) clean siblings — no false positives (#129)", () => {
  it("reports nothing and writes nothing when the stage stays in its worktree", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(worktree, "src", "handlers.ts"),
      "export function handle() { /* ok */ }\n"
    );
    fs.writeFileSync(path.join(worktree, "src", "new-thing.ts"), "export const x = 1;\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(report.artifactDir).toBeUndefined();
    expect(repoState(sibling).status).toBe("");
    expect(fs.existsSync(path.join(primary, CONTAINMENT_DIR))).toBe(false);
  });

  it("ignores .nightgauge/ artifacts the pipeline mirrors into the canonical root by design", async () => {
    // `writeDiagnosticWithMirror` and friends write stage diagnostics, exit
    // records and run state into the canonical checkout on purpose. Counting
    // them would make every single run report a breach against its own repo.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.mkdirSync(path.join(primary, ".nightgauge", "pipeline", "history", "129"), {
      recursive: true,
    });
    fs.writeFileSync(
      path.join(primary, ".nightgauge", "pipeline", "history", "129", "stall-diagnostic.json"),
      "{}\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
  });

  it("is a no-op when no other repo is configured (single-repo workspace)", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: primary,
      repoPaths: [primary],
    });
    expect(baseline.snapshots).toEqual([]);

    fs.writeFileSync(path.join(primary, "src", "handlers.ts"), "// legitimate\n");
    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches).toEqual([]);
  });
});

describe("retry — a rewrite of the same paths is still the stage's (#129)", () => {
  it("re-attributes paths a previous attempt already escaped with", async () => {
    // The incident: "A retry repeated it, rewriting the same paths." Without
    // the ledger the second attempt reads its own leftovers as pre-existing
    // operator dirt and passes silently.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const firstReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(firstReport.breaches).toHaveLength(1);

    // Retry: the paths are dirty at baseline time now.
    const second = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* v2 */ }\n"
    );

    const secondReport = await detectContainmentBreach({
      baseline: second,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(secondReport.breaches).toHaveLength(1);
    expect(secondReport.breaches[0].paths).toContain("src/handlers.ts");
    const patch = fs.readFileSync(secondReport.breaches[0].patchPath!, "utf-8");
    expect(patch).toContain("/* v2 */");
  });

  it("forgets a remembered path once it is clean again, so the operator's next edit is theirs", async () => {
    // Ledger rot would be a false-positive vector of its own: an entry left
    // behind after the operator recovered the patch and reverted the file would
    // make their NEXT edit of that file look like a stage write forever.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    await detectContainmentBreach({ baseline: first, stage: "feature-dev", issueNumber: 129 });

    // The operator recovers the work and cleans the repo.
    git(["checkout", "--", "."], sibling);
    fs.rmSync(path.join(sibling, "src", "api"), { recursive: true, force: true });
    expect(git(["status", "--porcelain", "-uall"], sibling)).toBe("");

    // Much later they start their own edit to one of those same files, and only
    // THEN does a stage run. That is pre-existing operator work.
    fs.writeFileSync(path.join(sibling, "src", "handlers.ts"), "// operator, weeks later\n");
    const later = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    const report = await detectContainmentBreach({ baseline: later, stage: "feature-validate" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(fs.readFileSync(path.join(sibling, "src", "handlers.ts"), "utf-8")).toBe(
      "// operator, weeks later\n"
    );
  });

  it("attributes a breach reported by both feature-dev and feature-validate on the same run (#127)", async () => {
    // #127's incident: feature-dev wrote across sibling repos and still
    // self-reported success; feature-validate ran next in the SAME sibling
    // working tree without cleaning up first. The ledger must attribute the
    // breach to whichever stage's detectContainmentBreach call runs, not
    // silently treat the second stage's baseline as "already dirty, not
    // ours" just because the first stage already recorded it.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const devReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 127,
    });
    expect(devReport.breaches).toHaveLength(1);

    // feature-validate runs next in the same worktree, same dirty sibling —
    // no new baseline capture between stages in the real pipeline. The
    // breach must still be attributed and reported.
    const validateReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-validate",
      issueNumber: 127,
    });
    expect(validateReport.breaches).toHaveLength(1);
    expect(validateReport.breaches[0].paths).toContain("src/handlers.ts");
  });
});

describe("parsePorcelainZ", () => {
  it("consumes the source field of a rename so following entries are not misparsed", () => {
    const stdout = "R  new.ts\0old.ts\0 M src/router.ts\0?? untracked.ts\0";
    expect(parsePorcelainZ(stdout)).toEqual([
      { code: "R ", path: "new.ts" },
      { code: " M", path: "src/router.ts" },
      { code: "??", path: "untracked.ts" },
    ]);
  });

  it("keeps paths with spaces intact (no git quoting to undo)", () => {
    expect(parsePorcelainZ("?? src/a file.ts\0")).toEqual([{ code: "??", path: "src/a file.ts" }]);
  });
});
