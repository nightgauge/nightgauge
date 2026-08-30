/**
 * #1168 — an adapter auth failure must reach a human.
 *
 * The incident: an operator restarted autonomous mode with the `claude` CLI
 * logged out. The pre-flight correctly refused all five dispatches at
 * pipeline-start at zero token cost — and then the fleet stopped with the
 * operator told nothing at all. `HeadlessOrchestrator` logged
 * `Adapter auth pre-flight failed` to the output channel and that was the
 * entire user-facing surface.
 *
 * These tests drive the REAL gate (`skip_auth_preflight` off, the SDK's
 * `runAdapterAuthPreflight` mocked so the verdict is deterministic instead of
 * shelling out to `claude auth status`) and assert on the operator surface the
 * gate now raises. The notice surface is injected, so what is asserted is the
 * DECISION — what the operator is told, and how many times — not the VSCode
 * host.
 *
 * ## Red-proof (behavioural neuter, never a compile error)
 *
 * Each test below names the neuter that turns it red. All four were observed
 * red before the fix landed; none of them is a deletion of a symbol.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { getSkipAuthPreflight } from "../../src/utils/nightgaugeConfig";
import { runAdapterAuthPreflight } from "@nightgauge/sdk";
import {
  resetAdapterAuthNotices,
  setAdapterAuthNoticeSurface,
  type AdapterAuthNoticeInput,
} from "../../src/utils/adapterAuthNotice";

vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: vi.fn(() => false),
}));

vi.mock("@nightgauge/sdk", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@nightgauge/sdk")>()),
  runAdapterAuthPreflight: vi.fn().mockResolvedValue({ ok: true, results: {}, failures: [] }),
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"#1168 test"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = () => Promise.resolve({ stdout: issueJson, stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

/**
 * The adapter CLI's probe output — an AUTH-STATE BLOB. The pre-flight puts it
 * in `AdapterAuthFailure.reason`, and it is exactly what must never reach a
 * toast. The fake token shape is deliberate: it is what a real
 * `claude auth status` dump looks like, and it is what the credential-hygiene
 * assertion greps for.
 */
const AUTH_STATE_BLOB =
  "claude auth status: {oauthAccount: null, apiKeySource: 'ANTHROPIC_API_KEY', " +
  "token: sk-ant-oat01-FAKE-DO-NOT-SURFACE, sessionKey: sk-ant-sid01-FAKE}";

const CLAUDE_REMEDY = "Run `claude auth login` (install via `brew install claude` if missing).";

function failingPreflight(timedOut = false) {
  return {
    ok: false,
    results: { "claude-headless": { ok: false, reason: AUTH_STATE_BLOB } },
    failures: [
      {
        adapter: "claude-headless",
        reason: AUTH_STATE_BLOB,
        suggestedFix: CLAUDE_REMEDY,
        timedOut,
      },
    ],
  };
}

const passingPreflight = { ok: true, results: {}, failures: [] };

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(null),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    beginRun: vi.fn(),
    endRun: vi.fn(),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    deferStage: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageContextFileSize: vi.fn().mockResolvedValue(undefined),
    updateTokens: vi.fn().mockResolvedValue(undefined),
    validateStageTransition: vi.fn().mockResolvedValue({ allowed: true }),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    clearBatchState: vi.fn().mockResolvedValue(undefined),
    batchUpdate: vi.fn().mockResolvedValue(undefined),
    isPaused: vi.fn().mockResolvedValue(false),
    recordExecutionOutcome: vi.fn().mockResolvedValue({ success: true }),
    setOutcomeType: vi.fn().mockResolvedValue(undefined),
    getBatchState: vi.fn().mockResolvedValue(null),
    clearRetrying: vi.fn().mockResolvedValue(undefined),
    markRetrying: vi.fn().mockResolvedValue(undefined),
    recordAutoRetry: vi.fn().mockResolvedValue(undefined),
    isPipelineComplete: vi.fn().mockReturnValue(false),
    recordToolCall: vi.fn(),
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
    hasBatchRunning: vi.fn().mockResolvedValue(false),
    getExecutionMode: vi.fn().mockResolvedValue("automatic"),
    resumePipeline: vi.fn().mockResolvedValue(undefined),
    pausePipeline: vi.fn().mockResolvedValue(undefined),
    setMeta: vi.fn(),
    setLabels: vi.fn().mockResolvedValue(undefined),
    recordBacktrack: vi.fn().mockResolvedValue(undefined),
    failPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator adapter auth operator surface (#1168)", () => {
  let mockLogger: Logger;
  let warnings: string[];
  let standing: AdapterAuthNoticeInput[][];
  let prevEnvSkip: string | undefined;

  function runPipelineFor(issueNumber: number) {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });
    return orchestrator.runPipeline(issueNumber);
  }

  beforeEach(() => {
    vi.clearAllMocks();
    // The suite-wide escape hatch in tests/setup.ts would skip the very gate
    // under test; clear it and restore in afterEach.
    prevEnvSkip = process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
    delete process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
    vi.mocked(getSkipAuthPreflight).mockReturnValue(false);

    resetAdapterAuthNotices();
    warnings = [];
    standing = [];
    setAdapterAuthNoticeSurface({
      warn: (message) => warnings.push(message),
      setStanding: (adapters) => standing.push(adapters),
    });

    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  afterEach(() => {
    resetAdapterAuthNotices();
    if (prevEnvSkip === undefined) delete process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
    else process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT = prevEnvSkip;
  });

  // RED-PROOF: comment out the `reportAdapterAuthFailure(...)` call in
  // HeadlessOrchestrator's pre-flight failure branch (the gate still fails the
  // run identically, so nothing stops compiling). Observed red: warnings is [].
  it("tells the operator which adapter failed and how to fix it", async () => {
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight() as never);

    const result = await runPipelineFor(1168);

    // The gate's own behaviour is unchanged: still fail-fast, still zero stages.
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");
    expect(vi.mocked(runStageSkillHeadless)).not.toHaveBeenCalled();

    expect(warnings).toHaveLength(1);
    // Names the adapter…
    expect(warnings[0]).toContain("claude-headless");
    // …and the remedy the pre-flight already knew.
    expect(warnings[0]).toContain("claude auth login");
    // …and says plainly that nothing will run until it is acted on. This is the
    // difference from the transient toasts beside it: an overload clears on its
    // own, an auth lapse cannot.
    expect(warnings[0]).toMatch(/no pipeline can start/i);

    // The standing condition is recorded, not just flashed.
    expect(standing.at(-1)).toEqual([
      { adapter: "claude-headless", suggestedFix: CLAUDE_REMEDY, timedOut: false },
    ]);
  });

  // RED-PROOF: in adapterAuthNotice.reportAdapterAuthFailure, change the dedupe
  // guard `if (outstanding.has(failure.adapter)) continue;` to `if (false)
  // continue;` — every adapter is then treated as fresh. Compiles; observed red
  // with warnings.length === 5.
  it("surfaces ONCE across a burst of concurrent dispatches, not once per issue", async () => {
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight() as never);

    // The incident exactly: five issues dispatched, all refused by the same
    // logged-out adapter. Each concurrent slot builds its own
    // HeadlessOrchestrator, so the dedupe has to be per-adapter and shared —
    // per-instance state would surface five times.
    const results = await Promise.all([1, 2, 3, 4, 5].map((n) => runPipelineFor(1000 + n)));

    expect(results.every((r) => r.failedStage === "pipeline-start")).toBe(true);
    expect(vi.mocked(runAdapterAuthPreflight)).toHaveBeenCalledTimes(5);
    expect(warnings).toHaveLength(1);
  });

  // RED-PROOF: in HeadlessOrchestrator's pre-flight PASS branch, comment out
  // `clearAdapterAuthNotice(adapters);`. Compiles and every other assertion in
  // this file still passes; observed red on both halves below — the standing
  // entry is never retracted, and the second lapse is swallowed by the stale
  // dedupe entry (warnings stays at 1).
  it("auto-resolves once the adapter authenticates, and re-arms for the next lapse", async () => {
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight() as never);
    await runPipelineFor(1168);
    expect(standing.at(-1)).toHaveLength(1);

    // The operator runs `claude auth login`. The next run's pre-flight passes.
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(passingPreflight as never);
    await runPipelineFor(1169);
    expect(standing.at(-1)).toEqual([]);

    // And a LATER lapse is told again rather than being swallowed by a stale
    // dedupe entry — the failure mode that turns "tell them once" into "tell
    // them once, ever".
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight() as never);
    await runPipelineFor(1170);
    expect(warnings).toHaveLength(2);
  });

  // RED-PROOF: in HeadlessOrchestrator's reportAdapterAuthFailure call, map
  // `suggestedFix: f.reason` instead of `f.suggestedFix` — a plausible slip,
  // since `reason` is the more descriptive-sounding field and the failure
  // object carries both. Compiles; observed red on the token assertions.
  it("never echoes the probe's auth-state payload", async () => {
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight() as never);
    await runPipelineFor(1168);

    const surfaced = [...warnings, ...standing.flat().map((a) => JSON.stringify(a))].join("\n");
    expect(surfaced).not.toContain("sk-ant-oat01");
    expect(surfaced).not.toContain("sk-ant-sid01");
    expect(surfaced).not.toContain("apiKeySource");
    expect(surfaced).not.toContain(AUTH_STATE_BLOB);

    // The blob is not censored out of existence — it stays in the diagnostic
    // log, where it always was and where it is useful.
    expect(vi.mocked(mockLogger.error)).toHaveBeenCalledWith(
      "Adapter auth pre-flight failed",
      expect.objectContaining({
        failures: [expect.objectContaining({ reason: AUTH_STATE_BLOB })],
      })
    );
  });

  // A probe TIMEOUT is a different sentence but the same remedy — the operator
  // must not be told "not authenticated" when auth may well be fine.
  it("distinguishes a probe timeout from a definitive logged-out negative", async () => {
    vi.mocked(runAdapterAuthPreflight).mockResolvedValue(failingPreflight(true) as never);
    await runPipelineFor(1168);

    expect(warnings[0]).toMatch(/could not be verified/i);
    expect(warnings[0]).not.toMatch(/is not authenticated/i);
    expect(warnings[0]).toContain("claude auth login");
  });
});
