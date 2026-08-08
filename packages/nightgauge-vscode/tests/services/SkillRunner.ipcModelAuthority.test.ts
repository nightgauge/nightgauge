/**
 * SkillRunner.ipcModelAuthority.test.ts (#340)
 *
 * On the VSCode IPC path the Go scheduler owns model resolution: escalation
 * after a failed stage, sticky model-unavailable downgrades (#42), the
 * `model_routing.minimum_model` floor (#366), the pr-merge haiku floor (#197)
 * and the feature-validate haiku gate all run in `resolveDispatchModel` and
 * arrive as `RunStageParams.model`. Before #340 the TS `SkillRunner` service
 * passed `undefined` for `modelOverride`, so `runStageSkillHeadless`
 * re-resolved from local config and the escalated tier never reached the
 * spawned CLI — the retry re-ran on the tier that had just failed.
 *
 * These tests drive the REAL `runStageSkillHeadless` through the IPC-path
 * service and assert on the process argv, because the argv is the only place
 * the defect is observable: every layer above it logged the right model.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn, execFileSync } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";
import { isSkillRenderCall, modelFromRenderArgs, skillRenderStdout } from "../helpers/skillRender";

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
    getExecutionAdapter: vi.fn((): string => process.env.NIGHTGAUGE_UI_CORE_ADAPTER || "claude"),
    // The LOCAL resolution the IPC path used to run. Every test below sets a
    // value here that differs from the model Go sends, so a passing assertion
    // can only mean the wire param won.
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
    // The REAL CODEX_TIER_MODEL_MAP values (packages/nightgauge-sdk/src/cli/
    // adapters/codexModelRegistry.ts). They matter literally: `gpt-5.6-sol`
    // serves BOTH the opus and fable registry bands, which is the input that
    // made the corpus book every correctly-served codex run as a routing miss
    // (see OutcomeActualBand, internal/orchestrator/outcome_semantics.go).
    resolveCodexPipelineModel: vi.fn((alias?: string) => {
      if (alias === "haiku") return "gpt-5.6-luna";
      if (alias === "sonnet") return "gpt-5.6-terra";
      if (alias === "opus" || alias === "fable") return "gpt-5.6-sol";
      return alias ?? "";
    }),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => ""),
    getCodexResumeEnabled: vi.fn(() => false),
    getCodexReasoningEffort: vi.fn(() => undefined),
    getSuperchargeCodexModel: vi.fn(() => undefined),
    getGeminiModel: vi.fn(() => "gemini-2.0-flash"),
    getGeminiAuthMethod: vi.fn(() => "api-key"),
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
import {
  getDefaultModel,
  getPerformanceMode,
  getStageEffort,
  getSuperchargeCodexModel,
} from "../../src/utils/incrediConfig";
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
    issueNumber: 340,
    model: "opus",
    timeout: 60_000,
    worktreeDir: "/test/workspace",
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

/** argv of the last `nightgauge skill render` invocation. */
function renderArgs(): string[] {
  const call = vi
    .mocked(execFileSync)
    .mock.calls.filter(([, a]) => isSkillRenderCall(a as string[]))
    .pop();
  expect(call, "skill render was never invoked").toBeDefined();
  return call![1] as string[];
}

const originalEnv = process.env;

beforeEach(() => {
  vi.clearAllMocks();
  process.env = { ...originalEnv, PATH: "/usr/local/bin:/usr/bin:/bin", VITEST: "true" };
  vi.mocked(fs.existsSync).mockReturnValue(true);
  vi.mocked(fs.readFileSync).mockReturnValue("");
  vi.mocked(execFileSync).mockImplementation(((_cmd: string, args: string[]) =>
    isSkillRenderCall(args) ? skillRenderStdout(args) : "") as never);
  vi.mocked(spawn).mockImplementation(() => createMockChildProcess());
  vi.mocked(getDefaultModel).mockReturnValue("sonnet");
  vi.mocked(getPerformanceMode).mockReturnValue("elevated");
  vi.mocked(getStageEffort).mockReturnValue(undefined);
  vi.mocked(getSuperchargeCodexModel).mockReturnValue(undefined);
});

afterEach(() => {
  killAllActiveProcesses();
  process.env = { ...originalEnv, VITEST: "true" };
});

/** env of the last spawn. */
function lastSpawnEnv(): Record<string, string> {
  const calls = vi.mocked(spawn).mock.calls;
  expect(calls.length, "spawn was not called").toBeGreaterThan(0);
  const opts = calls[calls.length - 1][2] as { env?: Record<string, string> };
  return opts?.env ?? {};
}

/** Start a stage and wait until the CLI has been spawned. */
async function dispatch(p: RunStageParams): Promise<{
  result: Promise<StageResult>;
  proc: ChildProcess;
}> {
  const proc = createMockChildProcess();
  vi.mocked(spawn).mockImplementation(() => proc);
  const runner = new SkillRunner(null, new Logger("SkillRunner-#340"));
  const result = runner.runStage(p);
  await vi.waitFor(() => expect(spawn).toHaveBeenCalled());
  return { result, proc };
}

/** Feed stream-json lines to the spawned process, then close it cleanly. */
function finish(proc: ChildProcess, lines: unknown[] = []): void {
  for (const line of lines) {
    proc.stdout!.emit("data", Buffer.from(`${JSON.stringify(line)}\n`));
  }
  proc.emit("close", 0);
}

describe("IPC path: the Go-resolved model is authoritative (#340)", () => {
  it("spawns on the escalated tier Go resolved, not the locally resolved one", async () => {
    // Go escalated feature-dev to opus after the sonnet attempt failed.
    // Local config still resolves sonnet — the tier that just failed.
    await dispatch(params({ model: "opus" }));

    expect(flagValue(claudeArgs(), "--model")).toBe("opus");
  });

  it("spawns on the sticky model-unavailable downgrade Go recorded (#42)", async () => {
    // The API rejected sonnet for this run, so Go's RetryEngine substituted
    // haiku for every later stage. Re-dispatching on sonnet would be refused
    // identically.
    await dispatch(params({ model: "haiku" }));

    expect(flagValue(claudeArgs(), "--model")).toBe("haiku");
  });

  it("keys the overlay render off the authoritative model too (#79/ADR 016)", async () => {
    await dispatch(params({ model: "opus" }));

    expect(modelFromRenderArgs(renderArgs())).toBe("opus");
    expect(flagValue(claudeArgs(), "--model")).toBe("opus");
  });

  it("fails the stage when the wire carries no model instead of resolving one locally", async () => {
    const runner = new SkillRunner(null, new Logger("SkillRunner-#340"));
    const result = await runner.runStage(params({ model: "" }));

    expect(result.success).toBe(false);
    expect(result.errorText).toContain("[ipc-contract]");
    expect(spawn).not.toHaveBeenCalled();
  });
});

describe("IPC path: served-model reporting is measured against the authoritative model (#340)", () => {
  it("forwards the served model when the CLI serves something other than what Go asked for", async () => {
    const { result, proc } = await dispatch(params({ model: "opus" }));
    finish(proc, [{ type: "system", subtype: "init", model: "claude-sonnet-4-6" }]);

    // scheduler.go re-records the stage on this value (`servedModel != model`).
    expect((await result).servedModel).toBe("claude-sonnet-4-6");
  });

  it("stays silent when the CLI serves exactly the model Go asked for", async () => {
    const { result, proc } = await dispatch(params({ model: "opus" }));
    finish(proc, [{ type: "system", subtype: "init", model: "opus" }]);

    // Terse by design: Go's dispatch-time attribution is already correct.
    expect((await result).servedModel).toBeUndefined();
  });

  it("stays silent when the stream reports no model at all", async () => {
    const { result, proc } = await dispatch(params({ model: "haiku" }));
    finish(proc);

    expect((await result).servedModel).toBeUndefined();
  });
});

describe("IPC path: adapter tier translation stays in TS and is reported back (#340)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
  });

  it("translates Go's tier band into the concrete Codex model", async () => {
    // Go speaks the registry BAND vocabulary its ladders are built on
    // (haiku|sonnet|opus|fable). Codex needs a concrete id, and only the
    // extension knows which adapter it selected — so the last-mile
    // translation stays here.
    await dispatch(params({ model: "opus" }));

    expect(lastSpawnEnv().NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.6-sol");
  });

  it("reports the translated model back so Go attributes the model that ran", async () => {
    const { result, proc } = await dispatch(params({ model: "opus" }));
    finish(proc);

    // The value is a CONCRETE id, deliberately — Go prices and attributes the
    // stage on it. Whether the run served the band the router predicted is
    // decided on the Go side, where the requested band is known: gpt-5.6-sol
    // serves [opus, fable], so `OutcomeActualBand(served, "opus")` books opus
    // (a HIT), not the strongest-band collapse to fable that made every
    // correctly-served codex run a routing miss. The booking itself is
    // asserted in TestRecordOutcome_ModelPairIsMeasuredNotCopied
    // (internal/orchestrator/outcome_corpus_parity_test.go).
    expect((await result).servedModel).toBe("gpt-5.6-sol");
  });

  it("reports the weaker Codex model for a sonnet-band dispatch", async () => {
    // The same pair in the other direction: gpt-5.6-terra serves sonnet only,
    // so a run dispatched at opus that reported this id books a real MISS.
    const { result, proc } = await dispatch(params({ model: "sonnet" }));
    finish(proc);

    expect(lastSpawnEnv().NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.6-terra");
    expect((await result).servedModel).toBe("gpt-5.6-terra");
  });

  // The adapter translation tables keyed off `modelDecision.source ===
  // "performance-mode"` — true only while resolveModel was the sole resolver.
  // With Go resolving (source "go-scheduler") that proxy reads false, so
  // without #340's mode/tier predicate a Maximum-mode run would silently fall
  // back to the adapter's configured default model.
  it("still applies the Maximum-mode Codex override when Go resolved the tier", async () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    // Deliberately NOT the tier translation ("opus" → "gpt-5.6-sol"), so the
    // assertion can only pass if the operator's override won.
    vi.mocked(getSuperchargeCodexModel).mockReturnValue("gpt-5.4");

    await dispatch(params({ model: "opus" }));

    expect(lastSpawnEnv().NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.4");
  });
});

describe("IPC path: Maximum-mode adapter mapping survives Go resolution (#340)", () => {
  it("maps the Go-resolved Maximum tier to the Gemini model", async () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    await dispatch(params({ model: "opus" }));

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
  });

  it("reports that Gemini model back for the corpus to resolve", async () => {
    // gemini-2.5-pro is the google provider's [opus, fable] model — the same
    // multi-band shape as codex's gpt-5.6-sol, and the same reason the corpus
    // resolves the band against the prediction instead of collapsing.
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    const { result, proc } = await dispatch(params({ model: "opus" }));
    finish(proc);

    expect((await result).servedModel).toBe("gemini-2.5-pro");
  });
});

// ── The wire field's VOCABULARY ────────────────────────────────────────────
//
// `RunStageParams.model` is a bare `string`, and two lookups downstream are
// keyed on the registry BAND set {haiku, sonnet, opus, fable}: whether the
// model takes `--effort`, and whether the active performance mode pinned this
// tier. Both were safe only while every caller reached them through
// `resolveModel`, which returns bands. Go emits a band today, but the wire
// cannot enforce that and the HeadlessOrchestrator path passes concrete ids
// (tests/services/SkillRunner.test.ts sends "claude-sonnet-4-20250514"), so the
// extension normalizes at its own boundary. Each assertion below fails silently
// without it: no error, no log line, just a missing flag or the adapter's
// default model.
describe("IPC path: a concrete registry id is understood, not silently mishandled (#340)", () => {
  it("still passes --effort when the wire carries a concrete model id", async () => {
    vi.mocked(getStageEffort).mockReturnValue("high");

    await dispatch(params({ model: "claude-sonnet-5" }));

    expect(flagValue(claudeArgs(), "--model")).toBe("claude-sonnet-5");
    // Without band normalization the band-keyed effort gate never matches, and
    // model_routing.stage_efforts / Maximum mode's effort:"high" vanish.
    expect(flagValue(claudeArgs(), "--effort")).toBe("high");
  });

  it("passes --effort for the band form too — the control", async () => {
    vi.mocked(getStageEffort).mockReturnValue("high");

    await dispatch(params({ model: "sonnet" }));

    expect(flagValue(claudeArgs(), "--effort")).toBe("high");
  });

  it("omits --effort for haiku in either spelling", async () => {
    vi.mocked(getStageEffort).mockReturnValue("high");

    await dispatch(params({ model: "claude-haiku-4-5-20251001" }));

    expect(claudeArgs()).not.toContain("--effort");
  });

  it("maps the Maximum-mode Gemini model from a concrete tier id", async () => {
    // reRouteContext writes `dev_model = rec.Model` — a concrete id — the
    // moment an operator switches to Maximum, so this is the shape the mode
    // predicate most has to handle.
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    await dispatch(params({ model: "claude-opus-5" }));

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
  });

  it("applies the Maximum-mode Codex override from a concrete tier id", async () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    vi.mocked(getSuperchargeCodexModel).mockReturnValue("gpt-5.4");

    await dispatch(params({ model: "claude-opus-5" }));

    expect(lastSpawnEnv().NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.4");
  });
});

// ── servedModel names what actually launched ──────────────────────────────
//
// scheduler.go re-records the stage — and, when the CLI reports no native cost,
// prices it — on this field. Reporting the extension's pre-spawn decision
// instead of the process's real model attributes history to a model that never
// ran.
describe("IPC path: servedModel is the model the adapter process was launched with (#340)", () => {
  it("reports the Codex supercharge override, not the tier translation", async () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    // Deliberately NOT the "opus" → "gpt-5.6-sol" tier translation: the codex
    // branch computes `heavyCodexOverride ?? modelDecision.model` and never
    // stamps the result back, so reporting modelDecision.model names
    // gpt-5.6-sol while gpt-5.4 is what ran.
    vi.mocked(getSuperchargeCodexModel).mockReturnValue("gpt-5.4");

    const { result, proc } = await dispatch(params({ model: "opus" }));
    finish(proc);

    expect(lastSpawnEnv().NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.4");
    expect((await result).servedModel).toBe("gpt-5.4");
  });

  it("reports the Gemini model the adapter launched when no mode mapping applies", async () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";

    const { result, proc } = await dispatch(params({ model: "sonnet" }));
    finish(proc);

    // The orchestrator asked for the sonnet band; the adapter launched its
    // configured model. History must name the latter.
    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.0-flash");
    expect((await result).servedModel).toBe("gemini-2.0-flash");
  });
});
