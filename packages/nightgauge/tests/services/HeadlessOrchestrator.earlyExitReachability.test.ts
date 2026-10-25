/**
 * verify-and-close must only close when the fix is reachable from the base
 * branch; work that exists only on the issue's own branch must not be closed
 * (#2509). Fails closed on git errors.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

const gitCalls: string[][] = [];
let revListImpl: (args: string[]) => string;

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (cmd !== "git") return Promise.resolve({ stdout: "", stderr: "" });
    gitCalls.push(args);
    try {
      return Promise.resolve({ stdout: revListImpl(args), stderr: "" });
    } catch (err) {
      return Promise.reject(err);
    }
  };
  return { ...actual, execFile: execFileMock };
});

type Reach = { onBase: boolean; uniqueCommits: number; base: string };

function makeOrchestrator(base: string | undefined = "main") {
  const state = {
    getBaseBranch: vi.fn().mockResolvedValue(base),
  } as unknown as PipelineStateService;
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
  const o = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 });
  return {
    logger,
    assess: () =>
      (
        o as unknown as { assessEarlyExitReachability(n: number): Promise<Reach> }
      ).assessEarlyExitReachability(2509),
  };
}

describe("assessEarlyExitReachability (#2509)", () => {
  beforeEach(() => {
    gitCalls.length = 0;
  });

  it("is on base when the branch has no commits beyond base", async () => {
    revListImpl = (a) => (a[0] === "rev-list" ? "0\n" : "");
    const { assess } = makeOrchestrator("main");
    expect(await assess()).toEqual({ onBase: true, uniqueCommits: 0, base: "main" });
    expect(gitCalls).toContainEqual(["rev-list", "--count", "origin/main..HEAD"]);
  });

  it("is NOT on base when the fix exists only on the issue branch", async () => {
    revListImpl = (a) => (a[0] === "rev-list" ? "3\n" : "");
    const { assess } = makeOrchestrator("main");
    expect(await assess()).toEqual({ onBase: false, uniqueCommits: 3, base: "main" });
  });

  it("uses the epic base branch", async () => {
    revListImpl = (a) => (a[0] === "rev-list" ? "0\n" : "");
    const { assess } = makeOrchestrator("epic/42-base");
    const r = await assess();
    expect(r.base).toBe("epic/42-base");
    expect(gitCalls).toContainEqual(["rev-list", "--count", "origin/epic/42-base..HEAD"]);
  });

  it("fails closed when git errors", async () => {
    revListImpl = () => {
      throw new Error("fatal: not a git repository");
    };
    const { assess, logger } = makeOrchestrator("main");
    const r = await assess();
    expect(r.onBase).toBe(false);
    expect(logger.warn).toHaveBeenCalled();
  });
});
