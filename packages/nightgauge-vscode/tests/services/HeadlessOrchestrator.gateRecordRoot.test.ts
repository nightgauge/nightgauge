/**
 * Issue #1021: every stage reported `[gate-not-invoked]` while the gates ran and
 * passed — #210 re-opening, with the detector it shipped firing on every
 * extension run since.
 *
 * The writer and the reader were wrong in the SAME direction, so they agreed
 * with each other and both disagreed with the daemon:
 *
 *   - `gate verify --record` writes DIRECTLY to `<workdir>/.nightgauge/pipeline`
 *     when it has no run identity to address the daemon with. The extension
 *     passed `--workdir <worktree>` and never passed `--run-id`, so the record
 *     went to a directory that holds no runtime snapshot.
 *   - The reader resolved `pinnedWorkspaceRoot ?? getWorkingDirectory()`, which
 *     prefers the worktree override — the same wrong place — so it read `{}` and
 *     the detector fired.
 *
 * The daemon is the single authoritative writer (#377) and files the snapshot
 * under the run's REPO root. Both halves now address it.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: () => true,
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

const { gateSpawns } = vi.hoisted(() => ({
  gateSpawns: [] as Array<{ args: string[]; cwd: string }>,
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[], opts?: { cwd?: string }) => {
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "gate") {
      gateSpawns.push({ args: [...args], cwd: opts?.cwd ?? "" });
      return Promise.resolve({
        stdout: JSON.stringify({
          stage: "pr-merge",
          gate_name: "pr-merge",
          passed: true,
          reason: "PR is MERGED",
          evidence: ["state=MERGED"],
        }),
        stderr: "",
      });
    }
    return Promise.resolve({ stdout: "{}", stderr: "" });
  };
  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });
  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(""),
    execFileSync: vi.fn().mockReturnValue("{}"),
  };
});

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi
      .fn()
      .mockImplementation((p: string) =>
        typeof p === "string" && p.includes("pr-") ? JSON.stringify({ pr_number: 4200 }) : "{}"
      ),
    writeFileSync: vi.fn(),
  };
});

const LAUNCH_ROOT = "/launch-root";
const REPO_ROOT = "/repos/target";
const WORKTREE = "/repos/target/.worktrees/issue-4151";
const RUN_ID = "01a02f24-498e-7364-bb8a-c96fa3739900";

function makeStateService(): PipelineStateService {
  return {
    getRunId: vi.fn().mockReturnValue(RUN_ID),
    getRunRepo: vi.fn().mockReturnValue("nightgauge/target"),
  } as unknown as PipelineStateService;
}

function makeOrchestrator() {
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
  const o = new HeadlessOrchestrator(makeStateService(), logger, { contextFileWaitMs: 0 });
  o.setMainRepoRoot(LAUNCH_ROOT);
  o.setRunRepoRoot(REPO_ROOT);
  o.setWorktreeOverride(WORKTREE);
  return o;
}

function flagValue(args: string[], flag: string): string | undefined {
  const i = args.indexOf(flag);
  return i >= 0 ? args[i + 1] : undefined;
}

describe("gate records address the daemon's root (#1021)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    gateSpawns.length = 0;
  });

  it("passes --run-id so the record routes through the daemon, not a direct worktree write", async () => {
    const o = makeOrchestrator();
    await (
      o as unknown as { verifyPostMergeState(n: number): Promise<Error | null> }
    ).verifyPostMergeState(4151);

    expect(gateSpawns.length, "the pr-merge gate should have been invoked").toBeGreaterThan(0);
    const spawn = gateSpawns[gateSpawns.length - 1];

    // Without a run id, recordGateResult falls through to
    // AppendStageGateResultToDisk under --workdir, which is the worktree.
    expect(spawn.args).toContain("--record");
    expect(flagValue(spawn.args, "--run-id")).toBe(RUN_ID);

    // --workdir stays the worktree on purpose: the gate's INPUTS live there.
    // Only the record's destination moves.
    expect(flagValue(spawn.args, "--workdir")).toBe(WORKTREE);
  });

  // #1054: --run-id alone was inert. recordGateResult derived BOTH the daemon
  // socket path and the direct-write state dir from --workdir, which is the
  // worktree — so the dial always failed and the fallback wrote into a
  // directory holding no run snapshot. The record reached nowhere on every
  // worktree run, and the end-of-run audit then reported [gate-not-invoked]
  // for gates that had demonstrably passed.
  //
  // The record's destination must be addressed separately from the gate's
  // inputs. This asserts both halves at once, because fixing it by moving
  // --workdir would make every gate false-negate.
  it("addresses the record at the repo root while the gate still reads the worktree", async () => {
    const o = makeOrchestrator();
    await (
      o as unknown as { verifyPostMergeState(n: number): Promise<Error | null> }
    ).verifyPostMergeState(4151);

    const spawn = gateSpawns[gateSpawns.length - 1];
    expect(flagValue(spawn.args, "--record-root")).toBe(REPO_ROOT);
    expect(flagValue(spawn.args, "--workdir")).toBe(WORKTREE);
  });

  // The duplicated --run-id block was harmless (cobra takes the last) but it
  // documented a fix that did not work. One occurrence, so a future reader is
  // not led back to the wrong mechanism.
  // #1054 round two. Round one asserted --record-root === REPO_ROOT and passed,
  // yet [gate-not-invoked] still fired in production on every run: the daemon
  // listens at the workspace root serve was started with, not at the run's repo
  // root, and in a multi-repo workspace those are different directories.
  //
  // The file fallback cannot substitute — the durable record is built by the
  // daemon from its in-memory RuntimeState and never reads that file — so the
  // dial reaching the daemon is the only path that lands a gate result.
  it("addresses the daemon at the persistent root, not the run's repo root", async () => {
    const o = makeOrchestrator();
    await (
      o as unknown as { verifyPostMergeState(n: number): Promise<Error | null> }
    ).verifyPostMergeState(4151);

    const spawn = gateSpawns[gateSpawns.length - 1];
    const daemonRoot = flagValue(spawn.args, "--daemon-root");

    expect(daemonRoot, "--daemon-root must be passed").toBeDefined();
    // All three roots are distinct concerns: inputs, record, socket.
    expect(flagValue(spawn.args, "--workdir")).toBe(WORKTREE);
    expect(flagValue(spawn.args, "--record-root")).toBe(REPO_ROOT);
    expect(daemonRoot).not.toBe(WORKTREE);
  });

  it("passes --run-id exactly once", async () => {
    const o = makeOrchestrator();
    await (
      o as unknown as { verifyPostMergeState(n: number): Promise<Error | null> }
    ).verifyPostMergeState(4151);

    const spawn = gateSpawns[gateSpawns.length - 1];
    expect(spawn.args.filter((a: string) => a === "--run-id")).toHaveLength(1);
  });

  it("reads gate results from the run's repo root, where the daemon writes them", async () => {
    const o = makeOrchestrator();
    const readSpy = vi.spyOn(
      o as unknown as { resolveRuntimeSnapshotPath(root: string, n: number): string | null },
      "resolveRuntimeSnapshotPath"
    );
    readSpy.mockReturnValue(null);

    (o as unknown as { readStageGateResultsForRun(n: number): unknown }).readStageGateResultsForRun(
      4151
    );

    expect(readSpy).toHaveBeenCalled();
    const rootUsed = readSpy.mock.calls[0][0];
    expect(
      rootUsed,
      "the reader must address the daemon's root; the worktree holds no snapshot"
    ).toBe(REPO_ROOT);
    expect(rootUsed).not.toBe(WORKTREE);
  });
});
