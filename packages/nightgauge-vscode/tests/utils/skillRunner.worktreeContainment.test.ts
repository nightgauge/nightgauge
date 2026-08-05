/**
 * skillRunner.worktreeContainment.test.ts
 *
 * Issue #129 — the WIRING test. `worktreeContainment.test.ts` proves the
 * detection and preservation logic; this file proves the pipeline actually
 * consults it and fails the stage.
 *
 * That distinction is the whole bug. In the incident the containment logic was
 * not "wrong" — it did not exist, so a `feature-dev` that wrote nine files into
 * two sibling repos exited 0 and was reported to the orchestrator as a SUCCESS.
 * Two stages later `feature-validate` failed the run with "no implementation
 * work detected", which is true of the branch and useless for triage.
 *
 * So the assertion that matters is on `onComplete`: the stage that escaped its
 * worktree must arrive at the orchestrator as `success: false` carrying a
 * reason that names the repo and the paths. On unmodified code this test fails
 * with `success: true`.
 *
 * The check is wired into `runStageSkillHeadless` rather than into an
 * orchestrator because that function is the single dispatch chokepoint — the Go
 * scheduler reaches it via `services/SkillRunner.ts` and the legacy
 * `HeadlessOrchestrator` via `_runSkillStageCore`. This test therefore covers
 * both execution paths.
 *
 * `spawn` is mocked (there is no Claude CLI in CI) but `execFile` is NOT: the
 * containment check runs real `git status` / `git diff` against real
 * repositories, because "did the pipeline notice, and is the work recoverable"
 * is not a question a mocked git can answer.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { execFileSync } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

let tmp: string;
let primary: string;
let worktree: string;
let sibling: string;

/**
 * Live workspace facts for the hoisted mocks. `vi.mock` factories are hoisted
 * above the `let` declarations above, so the per-test paths have to reach them
 * through a hoisted box rather than by closing over a normal binding.
 */
const ws = vi.hoisted(() => ({ worktree: "", repoPaths: [] as string[] }));

vi.mock("vscode", () => ({
  workspace: {
    // `findSkillFile` resolves SKILL.md against the VSCode workspace folder,
    // not the pinned stage root — point it at the worktree.
    get workspaceFolders() {
      return ws.worktree ? [{ uri: { fsPath: ws.worktree } }] : undefined;
    },
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: { getExtension: vi.fn(() => null) },
}));

// Mock ONLY `spawn`. `execFile` stays real so the containment check runs git
// for real — see the file header.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    ...actual,
    spawn: vi.fn(),
    // This suite runs REAL git against real temp repos, so execFileSync stays
    // real for everything except `skill render` (#79) — the fixture workspace
    // is a bare git repo with no skills/ tree, so a real render would
    // correctly fail to locate a SKILL.md and mask what the suite is testing.
    execFileSync: vi.fn((cmd: string, args: string[], opts: unknown) =>
      isSkillRenderCall(args)
        ? skillRenderStdout(args)
        : (actual.execFileSync as (...a: unknown[]) => unknown)(cmd, args, opts)
    ),
  };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: class RepositoryContextLoaderMock {
    static getInstance() {
      return {
        getCurrentRepository: () => null,
        getWorkingDirectory: () => ws.worktree,
        getAllRepositoryPaths: () => ws.repoPaths,
      };
    }
  },
}));

import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { CONTAINMENT_DIR, CONTAINMENT_ERROR_MARKER } from "../../src/utils/worktreeContainment";

function createMockChildProcess(): ChildProcess {
  const proc = new EventEmitter() as ChildProcess;
  proc.stdout = new EventEmitter() as NodeJS.ReadableStream;
  proc.stderr = new EventEmitter() as NodeJS.ReadableStream;
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
    destroyed: false,
  } as unknown as NodeJS.WritableStream;
  proc.kill = vi.fn();
  proc.killed = false;
  return proc;
}

function git(args: string[], cwd: string): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

function initRepo(dir: string): void {
  fs.mkdirSync(path.join(dir, "src"), { recursive: true });
  git(["init", "--quiet"], dir);
  git(["symbolic-ref", "HEAD", "refs/heads/main"], dir);
  git(["config", "user.email", "pipeline@nightgauge.test"], dir);
  git(["config", "user.name", "Nightgauge Test"], dir);
  git(["config", "commit.gpgsign", "false"], dir);
  fs.writeFileSync(path.join(dir, "src", "handlers.ts"), "export function handle() {}\n");
  fs.writeFileSync(path.join(dir, "src", "router.ts"), "export const routes = [];\n");
  git(["add", "-A"], dir);
  git(["commit", "--quiet", "-m", "base"], dir);
}

beforeEach(() => {
  vi.clearAllMocks();
  tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-contain-wire-")));
  primary = path.join(tmp, "primary");
  sibling = path.join(tmp, "sibling");
  fs.mkdirSync(primary, { recursive: true });
  fs.mkdirSync(sibling, { recursive: true });
  initRepo(primary);
  initRepo(sibling);
  // A real SKILL.md so the stage dispatch reaches `spawn` rather than
  // short-circuiting on skill discovery.
  const skillDir = path.join(primary, "skills", "nightgauge-feature-dev");
  fs.mkdirSync(skillDir, { recursive: true });
  fs.writeFileSync(
    path.join(skillDir, "SKILL.md"),
    "---\nname: nightgauge-feature-dev\nallowed-tools: Read Write Edit\n---\n# Feature Dev\n"
  );
  git(["add", "-A"], primary);
  git(["commit", "--quiet", "-m", "skills"], primary);

  worktree = path.join(primary, ".worktrees", "issue-129");
  git(["worktree", "add", "--quiet", "-b", "fix/129", worktree, "HEAD"], primary);
  ws.worktree = worktree;
  ws.repoPaths = [primary, sibling];
});

afterEach(() => {
  fs.rmSync(tmp, { recursive: true, force: true });
});

/**
 * Dispatch feature-dev pinned to the worktree and resolve on onComplete.
 *
 * Awaits the containment baseline before returning: in production the snapshot
 * overlaps only the CLI's multi-second start-up, but here the "stage" writes
 * its files microseconds after dispatch, so without the wait the baseline would
 * record the stage's own output as pre-existing state.
 */
async function runStage(): Promise<{
  proc: ChildProcess;
  completed: Promise<{ success: boolean; error?: Error }>;
  stderr: string[];
}> {
  const proc = createMockChildProcess();
  vi.mocked(spawn).mockReturnValue(proc);
  const stderr: string[] = [];
  let handle!: ReturnType<typeof runStageSkillHeadless>;
  const completed = new Promise<{ success: boolean; error?: Error }>((resolve) => {
    handle = runStageSkillHeadless(
      "feature-dev",
      129,
      {
        onStderr: (s: string) => stderr.push(s),
        onComplete: (r) => resolve({ success: r.success, error: r.error }),
      },
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      worktree
    );
  });
  if (handle.containmentBaselineReady) {
    await handle.containmentBaselineReady;
  } else {
    // No baseline to wait for. This is the PRE-#129 shape of the code, and the
    // fallback is here so that running this suite against it fails with the
    // real symptom — a stage that escaped its worktree reported as
    // `success: true` — rather than with a missing-field error that says
    // nothing about the bug. Never taken once the check is wired in; the
    // dedicated test below pins that.
    await new Promise((resolve) => setTimeout(resolve, 750));
  }
  return { proc, completed, stderr };
}

describe("runStageSkillHeadless — worktree write containment (#129)", () => {
  it("establishes the containment boundary before the stage can write", async () => {
    const proc = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(proc);
    const handle = runStageSkillHeadless(
      "feature-dev",
      129,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      worktree
    );
    expect(handle.containmentBaselineReady).toBeDefined();
    await handle.containmentBaselineReady;
    proc.emit("close", 0);
  });

  it("fails a stage that wrote into a sibling repo, even though it exited 0", async () => {
    const { proc, completed, stderr } = await runStage();

    // What the incident looked like: nothing in the worktree, real work in a
    // repo the stage does not own, and a clean exit code.
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* impl */ }\n"
    );
    fs.mkdirSync(path.join(sibling, "src", "api"), { recursive: true });
    fs.writeFileSync(path.join(sibling, "src", "api", "things.ts"), "export const things = 1;\n");

    proc.emit("close", 0);
    const result = await completed;

    // Pre-#129 this was `success: true` and the run died two stages later as
    // "no implementation work detected".
    expect(result.success).toBe(false);
    expect(result.error?.message).toContain(CONTAINMENT_ERROR_MARKER);
    expect(result.error?.message).toContain("wrote outside its worktree");
    expect(result.error?.message).toContain("sibling");
    expect(result.error?.message).toContain("src/handlers.ts");
    expect(result.error?.message).toContain("src/api/things.ts");
    expect(stderr.join("")).toContain(CONTAINMENT_ERROR_MARKER);
  });

  it("preserves the escaped work and leaves the sibling repo untouched", async () => {
    const { proc, completed } = await runStage();
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* impl */ }\n"
    );
    const beforeStatus = git(["status", "--porcelain", "-uall"], sibling);
    const beforeHead = git(["rev-parse", "HEAD"], sibling).trim();

    proc.emit("close", 0);
    await completed;

    // The patch is durable, under the canonical root that outlives the worktree.
    const containmentRoot = path.join(primary, CONTAINMENT_DIR);
    expect(fs.existsSync(containmentRoot)).toBe(true);
    const patches = fs
      .readdirSync(containmentRoot, { withFileTypes: true })
      .filter((e) => e.isDirectory())
      .flatMap((e) =>
        fs
          .readdirSync(path.join(containmentRoot, e.name))
          .filter((f) => f.endsWith(".patch"))
          .map((f) => path.join(containmentRoot, e.name, f))
      );
    expect(patches).toHaveLength(1);
    expect(fs.readFileSync(patches[0], "utf-8")).toContain("/* impl */");

    // And the sibling repo is byte-for-byte as the stage left it: no commit,
    // no staging, no revert.
    expect(git(["status", "--porcelain", "-uall"], sibling)).toBe(beforeStatus);
    expect(git(["rev-parse", "HEAD"], sibling).trim()).toBe(beforeHead);
    expect(fs.readFileSync(path.join(sibling, "src", "handlers.ts"), "utf-8")).toContain(
      "/* impl */"
    );
  });

  it("does not fail a stage that only wrote inside its own worktree", async () => {
    const { proc, completed } = await runStage();
    fs.writeFileSync(
      path.join(worktree, "src", "handlers.ts"),
      "export function handle() { /* impl */ }\n"
    );

    proc.emit("close", 0);
    const result = await completed;

    expect(result.success).toBe(true);
    expect(result.error).toBeUndefined();
    expect(fs.existsSync(path.join(primary, CONTAINMENT_DIR))).toBe(false);
  });

  it("does not fail a stage because a sibling repo was already dirty with operator work", async () => {
    // The operator is mid-edit in another repo. Nothing about that is the
    // stage's doing, and nothing about it may be attributed to the stage.
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work in progress\n");
    fs.writeFileSync(path.join(sibling, "operator-notes.md"), "scratch\n");
    const beforeStatus = git(["status", "--porcelain", "-uall"], sibling);

    const { proc, completed } = await runStage();
    fs.writeFileSync(path.join(worktree, "src", "handlers.ts"), "// stage work\n");
    proc.emit("close", 0);
    const result = await completed;

    expect(result.success).toBe(true);
    expect(result.error).toBeUndefined();
    expect(git(["status", "--porcelain", "-uall"], sibling)).toBe(beforeStatus);
    expect(fs.readFileSync(path.join(sibling, "src", "router.ts"), "utf-8")).toBe(
      "// operator work in progress\n"
    );
    expect(fs.existsSync(path.join(primary, CONTAINMENT_DIR))).toBe(false);
  });

  it("still reports the skill's own failure when the stage exited non-zero and stayed contained", async () => {
    const { proc, completed } = await runStage();
    proc.stderr!.emit("data", Buffer.from("boom\n"));
    proc.emit("close", 1);
    const result = await completed;

    expect(result.success).toBe(false);
    expect(result.error?.message).not.toContain(CONTAINMENT_ERROR_MARKER);
  });
});
