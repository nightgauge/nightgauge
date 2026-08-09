/**
 * SkillRunner.effortLadderAuthority.test.ts (#336, #75)
 *
 * The `--effort` LEVEL check must be made against the ladder of the model the
 * process is actually launched with — `args.push("--model", modelDecision.model)`
 * ships a CONCRETE id, and a concrete id can be a deprecated sibling of the
 * band's current leader.
 *
 * Validating the BAND's ladder instead reads the wrong row of the registry:
 * `claude-opus-4-8` resolves to band `opus`, whose current leader is
 * `claude-opus-5` — and Opus 5 is the only model that accepts `max`. The check
 * passed, `--effort max` was appended next to `--model claude-opus-4-8`, and the
 * spawn carried a level that model does not have. Exactly the silent-drift class
 * #75 exists to prevent, arriving through the gate #75 installed.
 *
 * These tests drive the REAL `runStageSkillHeadless` through the IPC-path
 * service (same style as SkillRunner.ipcModelAuthority.test.ts) and assert on
 * process argv, because argv is the only place the defect is observable: every
 * layer above it logs a model and an effort that individually look fine.
 *
 * They also pin the delivery mechanism. `runStageSkillHeadless` returns a handle
 * and reports failure through callbacks; a raw `throw` from the assert escapes
 * past `bootstrap/services.ts`, which calls it unguarded. The stage must still
 * FAIL — never warn-and-continue, never downgrade — but through the file's
 * established `[stage:...]` envelope (the #4021 model-preflight pattern).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn, execFileSync } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";
import { isSkillRenderCall, skillRenderStdout } from "../helpers/skillRender";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(() => ""),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
}));

vi.mock("child_process", async () => {
  const { isSkillRenderCall: isRender, skillRenderStdout: renderOut } =
    await import("../helpers/skillRender");
  return {
    spawn: vi.fn(),
    execFileSync: vi.fn((_cmd: string, args: string[]) => (isRender(args) ? renderOut(args) : "")),
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

vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
  return {
    ...actual,
    precomputeCalibratedStallThresholds: vi.fn().mockResolvedValue(undefined),
    getAuthProvider: vi.fn(() => "max"),
    getExecutionAdapter: vi.fn((): string => "claude"),
    getDefaultModel: vi.fn(() => "sonnet"),
    getStageModel: vi.fn(() => undefined),
    getStageEffort: vi.fn(() => undefined),
    getStageOverrideModel: vi.fn(() => undefined),
    getModelRoutingMode: vi.fn(() => "automatic"),
    getPerformanceMode: vi.fn(() => "elevated"),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getStageMcpTools: vi.fn(() => []),
    getMcpToolsConfig: vi.fn(() => []),
    getLargeDiffThreshold: vi.fn(() => 500),
    getExperimentConfig: vi.fn(() => undefined),
    getConfidenceThreshold: vi.fn(() => 0.5),
    getMinimumModel: vi.fn(() => undefined),
    getStageModelsMatrix: vi.fn(() => undefined),
    getTypeOverrides: vi.fn(() => undefined),
    getGitHubAuthToken: vi.fn(() => null),
    getGitHubAuthTokens: vi.fn(() => ({})),
    getGitHubUser: vi.fn(() => null),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => ""),
    getCodexResumeEnabled: vi.fn(() => false),
    getCodexReasoningEffort: vi.fn(() => undefined),
    getSuperchargeCodexModel: vi.fn(() => undefined),
  };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: {
    getInstance: vi.fn(() => ({
      getCurrentRepository: vi.fn().mockReturnValue(null),
      getWorkingDirectory: vi.fn().mockReturnValue("/test/workspace"),
    })),
  },
}));

import { SkillRunner, type RunStageParams, type StageResult } from "../../src/services/SkillRunner";
import { killAllActiveProcesses } from "../../src/utils/skillRunner";
import { Logger } from "../../src/utils/logger";
import { getPerformanceMode, getStageEffort } from "../../src/utils/incrediConfig";
import type { ClaudeEffort } from "../../src/utils/resolvers/stageResolver";
import type { PipelineStage } from "@nightgauge/sdk";

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

function params(overrides: Partial<RunStageParams> = {}): RunStageParams {
  return {
    stage: "feature-dev" as PipelineStage,
    issueNumber: 336,
    model: "claude-opus-5",
    timeout: 60_000,
    worktreeDir: "/test/workspace",
    // Required since ADR-017 step 0b: the Go emitter never dispatches a stage
    // without a run identity, so no caller of this type has one to omit.
    runId: "01890a5d-ac96-774b-bcce-b302099a8057",
    ...overrides,
  };
}

/** argv of the last `claude` spawn. */
function claudeArgs(): string[] {
  const call = vi
    .mocked(spawn)
    .mock.calls.filter(([cmd]) => cmd === "claude")
    .pop();
  expect(call, "claude was never spawned").toBeDefined();
  return call![1] as string[];
}

function flagValue(args: string[], flag: string): string | undefined {
  const i = args.indexOf(flag);
  return i >= 0 ? args[i + 1] : undefined;
}

/**
 * Every command line the run spawned, flattened. Asserted against `[]` rather
 * than via `not.toHaveBeenCalled()` so a regression PRINTS the offending argv —
 * `--model claude-opus-4-8 ... --effort max` is the defect, stated.
 */
function spawnedCommandLines(): string[] {
  return vi.mocked(spawn).mock.calls.map(([cmd, a]) => [cmd, ...((a as string[]) ?? [])].join(" "));
}

const originalEnv = process.env;

beforeEach(() => {
  vi.clearAllMocks();
  process.env = { ...originalEnv, PATH: "/usr/local/bin:/usr/bin:/bin", VITEST: "true" };
  vi.mocked(fs.existsSync).mockReturnValue(true);
  vi.mocked(fs.readFileSync).mockReturnValue("");
  vi.mocked(execFileSync).mockImplementation(((_cmd: string, args: string[]) =>
    isSkillRenderCall(args) ? skillRenderStdout(args) : "") as never);
  // Auto-close every spawn on the next tick. A stage that reaches the CLI
  // therefore RESOLVES (successfully) instead of hanging to the suite timeout —
  // so a run that should have been rejected pre-spawn fails these tests on the
  // assertion, naming the defect, rather than on a 15s timeout.
  vi.mocked(spawn).mockImplementation(() => {
    const proc = createMockChildProcess();
    setImmediate(() => proc.emit("close", 0));
    return proc;
  });
  vi.mocked(getPerformanceMode).mockReturnValue("elevated");
  vi.mocked(getStageEffort).mockReturnValue(undefined);
});

afterEach(() => {
  killAllActiveProcesses();
  process.env = { ...originalEnv, VITEST: "true" };
});

function run(p: RunStageParams, effort: ClaudeEffort): Promise<StageResult> {
  vi.mocked(getStageEffort).mockReturnValue(effort);
  return new SkillRunner(null, new Logger("SkillRunner-#336")).runStage(p);
}

// ── The launched model's own ladder decides ────────────────────────────────
//
// Both models below are DEPRECATED registry entries that a run can still be
// pinned to (an operator `--model`, a dated pin, a Go-scheduler wire value).
// Each declares a shorter ladder than the current leader of its band, so the
// band-keyed check and the model-keyed check disagree — which is the whole
// observable difference.
describe("effort validation is keyed on the DISPATCHED model, not its band (#336/#75)", () => {
  it("rejects max on claude-opus-4-8, whose ladder stops at xhigh", async () => {
    // Band `opus` leads with claude-opus-5, the only model that accepts `max`.
    const result = await run(params({ model: "claude-opus-4-8" }), "max");

    // Nothing may reach the CLI — asserted first, because the spawned argv is
    // where the band-keyed check is observable.
    expect(spawnedCommandLines(), "a CLI was spawned with an off-ladder effort").toEqual([]);
    expect(result.success).toBe(false);
    expect(result.errorText).toContain("[stage:effort-unsupported]");
    // The envelope has to name all three facts, or triage cannot tell an
    // unsupported level from an unknown model.
    expect(result.errorText).toContain("claude-opus-4-8");
    expect(result.errorText).toContain("max");
    expect(result.errorText).toContain("xhigh");
  });

  it("rejects xhigh on claude-sonnet-4-6, whose ladder stops at high", async () => {
    // Band `sonnet` leads with claude-sonnet-5, which does accept xhigh.
    const result = await run(params({ model: "claude-sonnet-4-6" }), "xhigh");

    expect(spawnedCommandLines(), "a CLI was spawned with an off-ladder effort").toEqual([]);
    expect(result.success).toBe(false);
    expect(result.errorText).toContain("[stage:effort-unsupported]");
    expect(result.errorText).toContain("claude-sonnet-4-6");
    expect(result.errorText).toContain("xhigh");
  });

  it("still dispatches max on claude-opus-5, which declares it — no false positive", async () => {
    // The control. A model-keyed check that rejected this too would be a
    // stricter bug, not a fix.
    const result = await run(params({ model: "claude-opus-5" }), "max");

    expect(result.success).toBe(true);
    expect(flagValue(claudeArgs(), "--model")).toBe("claude-opus-5");
    expect(flagValue(claudeArgs(), "--effort")).toBe("max");
  });
});
