/**
 * skillRunner.stageBudget.test.ts (Issue #1668)
 *
 * Non-USD stage budgets and context-window telemetry for stages the editor
 * launches itself through `runStageSkillHeadless`:
 *
 *   - the enforcer, replaying OpenCode activity streams: the turn and token
 *     budgets stop a stage at the event that crosses them, not at EOF, and
 *     the per-step peak is a max, never a sum;
 *   - the real dispatch path at the `child_process.spawn` seam: a breach
 *     terminates the stage and fails it with `stage_budget_exceeded:<dim>`,
 *     the wall clock stops a stage that streams continuously, a zero-cost
 *     stage whose budgets do not resolve is refused, and the peak and window
 *     reach the result;
 *   - the process-tree kill against real processes lives in
 *     stageBudget.processTree.test.ts, which needs the real child_process.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import * as fs from "fs";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
}));

vi.mock("child_process", async () => {
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) =>
      isSkillRenderCall(args) ? skillRenderStdout(args) : ""
    ),
    execFile: vi.fn(
      (
        _cmd: string,
        _args: string[],
        _opts: unknown,
        cb: (e: Error | null, s: string, t: string) => void
      ) => {
        cb(new Error("no children"), "", "");
      }
    ),
  };
});

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/nightgaugeConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/nightgaugeConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getPerformanceMode: vi.fn(() => "elevated"),
  };
});

const LOCAL_MODEL = "lm-studio/qwen/qwen3.8-27b";

vi.mock("../../src/utils/resolvers/modelResolver", async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    "../../src/utils/resolvers/modelResolver"
  );
  return { ...actual, getOpenCodeModel: vi.fn(() => "lm-studio/qwen/qwen3.8-27b") };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: {
    getInstance: vi.fn(() => ({
      getCurrentRepository: vi.fn().mockReturnValue(null),
      getWorkingDirectory: vi.fn().mockReturnValue("/test/workspace"),
    })),
  },
}));

import { runStageSkillHeadless, killAllActiveProcesses } from "../../src/utils/skillRunner";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import {
  StageBudgetEnforcer,
  setStageBudgetResolver,
  type StageBudgetBreach,
  type StageBudgets,
} from "../../src/utils/stageBudget";
import { createMockChildProcess, type MockChildProcess } from "../mocks/child-process";

const MOCK_SKILL_CONTENT = `---
name: test-skill
allowed-tools: Read Write Edit
---
# Test Skill
`;

/** The SDK stage CLI's activity line for one OpenCode event (#1657, #1668). */
function activity(
  event: string,
  extra: { reason?: string; tokens?: Record<string, number> } = {}
): string {
  return JSON.stringify({
    level: "debug",
    message: "adapter activity",
    data: { adapter: "opencode", event, ...extra },
  });
}

function stepFinish(
  input: number,
  cacheRead = 0,
  cacheWrite = 0,
  output = 0,
  reason = "tool-calls"
) {
  return activity("step_finish", {
    reason,
    tokens: { input, output, reasoning: 0, cacheRead, cacheWrite },
  });
}

const BUDGETS: StageBudgets = {
  maxTurns: 200,
  maxWallClockMs: 4 * 3600_000,
  maxTokens: 25_000_000,
  zeroCost: true,
  contextWindowTokens: 131072,
};

describe("StageBudgetEnforcer replaying an OpenCode stream (#1668)", () => {
  function replay(lines: string[], limits: StageBudgets) {
    const breaches: Array<{ breach: StageBudgetBreach; atEvent: number }> = [];
    let at = 0;
    const e = new StageBudgetEnforcer({
      format: "sdk",
      onBreach: (breach) => breaches.push({ breach, atEvent: at }),
    });
    e.start();
    e.arm(limits);
    for (const line of lines) {
      at++;
      e.observeLine(line);
      if (breaches.length > 0) break;
    }
    e.disarm();
    return { e, breaches };
  }

  it("stops at the 5th of 8 step_finish events under maxTurns 5", () => {
    const lines = Array.from({ length: 8 }, () => stepFinish(100));
    const { breaches } = replay(lines, { ...BUDGETS, maxTurns: 5 });
    expect(breaches).toHaveLength(1);
    expect(breaches[0].atEvent).toBe(5);
    expect(breaches[0].breach).toEqual({ dimension: "turns", observed: 5, ceiling: 5 });
  });

  it("does not stop a stage whose last allowed turn is its final answer", () => {
    const lines = [stepFinish(100), stepFinish(100), stepFinish(100, 0, 0, 0, "stop")];
    const { breaches } = replay(lines, { ...BUDGETS, maxTurns: 3 });
    expect(breaches).toHaveLength(0);
  });

  it("stops right after the event whose cumulative tokens cross 50 000, not at EOF", () => {
    // 7 000 input + 500 output + 500 cache write = 8 000 counted per step:
    // 48 000 after event 6, 56 000 after event 7.
    const lines = Array.from({ length: 10 }, () => stepFinish(7000, 20000, 500, 500));
    const { breaches, e } = replay(lines, { ...BUDGETS, maxTokens: 50_000 });
    expect(breaches).toHaveLength(1);
    expect(breaches[0].atEvent).toBe(7);
    expect(breaches[0].breach).toEqual({ dimension: "tokens", observed: 56_000, ceiling: 50_000 });
    // Cache reads never count against the token budget.
    expect(e.tokensUsed).toBe(56_000);
  });

  it("takes the peak as the max step prompt, never the sum", () => {
    const { e, breaches } = replay(
      [stepFinish(7550), stepFinish(2010, 9000, 1000), stepFinish(9800)],
      BUDGETS
    );
    expect(breaches).toHaveLength(0);
    expect(e.peakStepInputTokens).toBe(12010);
    expect(e.peakStepInputTokens).not.toBe(29360);
    expect(e.contextWindowTokens).toBe(131072);
  });

  it("judges what the stream showed before the budget arrived", () => {
    const breaches: StageBudgetBreach[] = [];
    const e = new StageBudgetEnforcer({ format: "sdk", onBreach: (b) => breaches.push(b) });
    e.start();
    for (let i = 0; i < 6; i++) e.observeLine(stepFinish(100));
    expect(breaches).toHaveLength(0);
    e.arm({ ...BUDGETS, maxTurns: 5 });
    expect(breaches).toEqual([{ dimension: "turns", observed: 6, ceiling: 5 }]);
  });

  it("counts a claude assistant message once by its id, and not a subagent's", () => {
    const breaches: StageBudgetBreach[] = [];
    const e = new StageBudgetEnforcer({ format: "claude", onBreach: (b) => breaches.push(b) });
    e.start();
    e.arm({ ...BUDGETS, maxTurns: 2 });
    const msg = (id: string, type: string, parent?: string) =>
      JSON.stringify({
        type: "assistant",
        ...(parent ? { parent_tool_use_id: parent } : {}),
        message: { id, content: [{ type }] },
      });
    e.observeLine(msg("m1", "text"));
    e.observeLine(msg("m1", "tool_use"));
    e.observeLine(msg("sub", "tool_use", "toolu_1"));
    expect(e.turnCount).toBe(1);
    e.observeLine(msg("m2", "tool_use"));
    expect(breaches).toEqual([{ dimension: "turns", observed: 2, ceiling: 2 }]);
  });
});

// ── The real dispatch path ────────────────────────────────────────────────

const originalEnv = process.env;
let proc: MockChildProcess;

/** A mock child whose SIGTERM ends it, as a real stage CLI's would. */
function killableChild(pid: number): MockChildProcess {
  const child = createMockChildProcess();
  Object.defineProperty(child, "pid", { value: pid, writable: true });
  Object.defineProperty(child, "exitCode", { value: null, writable: true });
  Object.defineProperty(child, "signalCode", { value: null, writable: true });
  child.kill = vi.fn((sig?: NodeJS.Signals | number) => {
    setTimeout(() => {
      Object.defineProperty(child, "signalCode", { value: sig ?? "SIGTERM", writable: true });
      child.emit("exit", null, sig);
      child.emit("close", null, sig);
    }, 5);
    return true;
  }) as MockChildProcess["kill"];
  return child;
}

function dispatch(callbacks: Parameters<typeof runStageSkillHeadless>[2] = {}) {
  return runStageSkillHeadless(
    "feature-dev",
    42,
    callbacks,
    undefined,
    undefined,
    undefined,
    LOCAL_MODEL
  );
}

function dispatchUntilComplete(): {
  complete: Promise<SkillRunResult>;
  stderr: string[];
} {
  const stderr: string[] = [];
  const complete = new Promise<SkillRunResult>((resolve) => {
    dispatch({ onComplete: resolve, onStderr: (s) => stderr.push(s) });
  });
  return { complete, stderr };
}

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  vi.clearAllMocks();
  process.env = {
    ...originalEnv,
    PATH: "/usr/local/bin:/usr/bin:/bin",
    VITEST: "true",
    NIGHTGAUGE_EXPERIMENTAL_OPENCODE: "1",
    NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV: "opencode",
  };
  vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
    const filePath = String(p);
    return (
      filePath.includes("SKILL.md") ||
      filePath.includes("skills/") ||
      filePath.includes("nightgauge-sdk/dist/cli/index.js")
    );
  });
  vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
  proc = killableChild(4_000_000);
  vi.mocked(spawn).mockReturnValue(proc);
});

afterEach(() => {
  setStageBudgetResolver(null);
  killAllActiveProcesses();
  process.env = { ...originalEnv, VITEST: "true" };
  vi.restoreAllMocks();
});

describe("runStageSkillHeadless under a stage budget (#1668)", () => {
  it("asks Go for the budget with the dispatch's stage, adapter and model", async () => {
    const resolver = vi.fn(async () => BUDGETS);
    setStageBudgetResolver(resolver);
    dispatch();
    await flush();
    expect(resolver).toHaveBeenCalledWith({
      repo: "",
      stage: "feature-dev",
      adapter: "opencode",
      model: LOCAL_MODEL,
    });
  });

  it("stops the stage at its 5th turn and fails it with stage_budget_exceeded:turns", async () => {
    setStageBudgetResolver(async () => ({ ...BUDGETS, maxTurns: 5 }));
    const { complete } = dispatchUntilComplete();
    await flush();
    for (let i = 1; i <= 8; i++) {
      proc.stdout.emit("data", Buffer.from(`${stepFinish(100)}\n`));
      if (i === 4) {
        await flush();
        expect(proc.kill).not.toHaveBeenCalled();
      }
    }
    await vi.waitFor(() => expect(proc.kill).toHaveBeenCalledWith("SIGTERM"));
    const result = await complete;
    expect(result.success).toBe(false);
    expect(result.error?.message).toMatch(/^stage_budget_exceeded:turns observed=5 ceiling=5/);
    expect(result.signalSource).toBe("stage-budget");
  });

  it("passes a resolved turn budget to OpenCode's steps cap on the next dispatch", async () => {
    setStageBudgetResolver(async () => ({ ...BUDGETS, maxTurns: 17 }));
    dispatch();
    await flush();
    proc.emit("close", 0);
    await flush();
    proc = killableChild(4_000_001);
    vi.mocked(spawn).mockReturnValue(proc);
    dispatch();
    const calls = vi.mocked(spawn).mock.calls;
    const env = (calls[calls.length - 1][2] as { env: Record<string, string> }).env;
    expect(env.NIGHTGAUGE_STAGE_MAX_TURNS).toBe("17");
  });

  it("stops a stage that streams continuously at its wall clock, not its stage timeout", async () => {
    setStageBudgetResolver(async () => ({ ...BUDGETS, maxWallClockMs: 1000 }));
    const started = Date.now();
    const { complete } = dispatchUntilComplete();
    const ticker = setInterval(() => {
      proc.stdout.emit("data", Buffer.from(`${activity("tool_use")}\n`));
    }, 100);
    try {
      const result = await complete;
      const elapsed = Date.now() - started;
      expect(elapsed).toBeGreaterThanOrEqual(900);
      expect(elapsed).toBeLessThan(3000);
      expect(result.success).toBe(false);
      expect(result.error?.message).toMatch(/^stage_budget_exceeded:wall_clock /);
    } finally {
      clearInterval(ticker);
    }
  });

  it("refuses a zero-cost stage whose budgets do not resolve", async () => {
    setStageBudgetResolver(async () => {
      throw new Error("ipc down");
    });
    // The first dispatch of a shape resolves while its process starts, so it
    // is stopped as soon as the resolution fails, before any turn.
    const first = dispatchUntilComplete();
    const firstResult = await first.complete;
    expect(firstResult.success).toBe(false);
    expect(firstResult.error?.message).toMatch(/\[stage-budget\] refused: .*ipc down/);
    expect(vi.mocked(spawn)).toHaveBeenCalledTimes(1);

    // Every later dispatch of it is refused before spawn.
    vi.mocked(spawn).mockClear();
    const second = dispatchUntilComplete();
    const secondResult = await second.complete;
    expect(vi.mocked(spawn)).toHaveBeenCalledTimes(0);
    expect(secondResult.success).toBe(false);
    expect(secondResult.error?.message).toMatch(/\[stage-budget\] refused: /);
  });

  it("runs a priced stage whose budgets do not resolve under its USD caps, with a warning", async () => {
    setStageBudgetResolver(async () => {
      throw new Error("ipc down");
    });
    const stderr: string[] = [];
    runStageSkillHeadless(
      "feature-dev",
      42,
      { onStderr: (s) => stderr.push(s) },
      undefined,
      undefined,
      undefined,
      "anthropic/claude-sonnet-5"
    );
    await flush();
    await flush();
    expect(proc.kill).not.toHaveBeenCalled();
    expect(stderr.join("")).toContain("did not resolve (ipc down)");
  });

  it("reports the per-step peak and the window on the result, omitting them when unobserved", async () => {
    setStageBudgetResolver(async () => BUDGETS);
    const { complete } = dispatchUntilComplete();
    await flush();
    for (const line of [
      stepFinish(7550),
      stepFinish(2010, 9000, 1000),
      stepFinish(9800, 0, 0, 0, "stop"),
    ]) {
      proc.stdout.emit("data", Buffer.from(`${line}\n`));
    }
    proc.emit("close", 0);
    const result = await complete;
    expect(result.peakStepInputTokens).toBe(12010);
    expect(result.contextWindowTokens).toBe(131072);

    setStageBudgetResolver(null);
    proc = killableChild(4_000_002);
    vi.mocked(spawn).mockReturnValue(proc);
    const bare = dispatchUntilComplete();
    proc.emit("close", 0);
    const bareResult = await bare.complete;
    expect(bareResult).not.toHaveProperty("peakStepInputTokens");
    expect(bareResult).not.toHaveProperty("contextWindowTokens");
  });
});
