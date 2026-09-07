/**
 * skillRunner.fallback.test.ts
 *
 * Integration tests for the auth-aware adapter fallback walker (Issue #3231).
 *
 * The skillRunner integrates `walkAdapterFallback` and the prereq probe to:
 *   - Walk the effective fallback chain at stage start when the primary fails.
 *   - Emit a per-hop info log line in the AC #4 format.
 *   - Choose between [stage:adapter-unavailable] (primary-only failure or
 *     strict mode) and [stage:no-adapter-available] (full chain exhausted).
 *   - Propagate `adapterFallbackChainUsed` on `onComplete.adapterDecision`.
 *
 * Driving the prereq-failure path through the live `validateAdapterPrerequisites`
 * is awkward — `commandExists` short-circuits to `true` under VITEST so claude
 * always passes. So we mock `walkAdapterFallback` directly and force the
 * primary-fail / fallback-success / chain-exhausted shapes per test.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
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
  extensions: { getExtension: vi.fn(() => null) },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

vi.mock("child_process", async () => {
  // Since #79 the extension composes no skill text of its own: it shells out
  // to `nightgauge skill render`. Answer that one call with the shared
  // envelope stub; every other execFileSync caller keeps an empty result.
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
    exists: true,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/nightgaugeConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/nightgaugeConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getExecutionAdapter: vi.fn(() => "claude"),
    getDefaultModel: vi.fn(() => undefined),
    getStageModel: vi.fn(() => undefined),
    getStageEffort: vi.fn(() => undefined),
    getCodexModel: vi.fn(() => "gpt-5.4"),
    resolveCodexPipelineModel: vi.fn(() => "gpt-5.4"),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => undefined),
    getCodexResumeEnabled: vi.fn(() => false),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getStageMcpTools: vi.fn(() => []),
    getMcpToolsConfig: vi.fn(() => []),
    getGitHubAuthToken: vi.fn(() => null),
    getGitHubAuthTokens: vi.fn(() => ({})),
    getGitHubUser: vi.fn(() => null),
  };
});

vi.mock("../../src/services/RepositoryContextLoader", () => ({
  RepositoryContextLoader: class {
    static getInstance() {
      return {
        getCurrentRepository: () => null,
        getWorkingDirectory: () => "/test/workspace",
      };
    }
  },
}));

// The contract under test — drive walker behavior per scenario.
// `vi.mock` is hoisted above all top-level statements, so the mock factory
// cannot close over a top-level `const`. Use `vi.hoisted` so the mock fn is
// also lifted and is available when the factory runs.
const { walkAdapterFallbackMock, resolveStageAdapterMock } = vi.hoisted(() => ({
  walkAdapterFallbackMock: vi.fn(),
  resolveStageAdapterMock: vi.fn(() => ({ adapter: "lm-studio", source: "stage-config" })),
}));
// Force primary prereq failure for `lm-studio` only, so we can pick this
// adapter as the primary in tests that need a prereq failure. Everything
// else passes (matching the real-world `commandExists` short-circuit under
// VITEST). For prereq tests we pin `lm-studio` and let the walker decide
// the rest.
vi.mock("../../src/utils/resolvers/adapterResolver", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/resolvers/adapterResolver")>(
    "../../src/utils/resolvers/adapterResolver"
  );
  return {
    ...actual,
    resolveStageAdapter: resolveStageAdapterMock,
    walkAdapterFallback: walkAdapterFallbackMock,
  };
});

import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { createMockChildProcess } from "../mocks/child-process";

let mockProcess: ChildProcess;

describe("skillRunner — adapter fallback walker integration (Issue #3231)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
description: test
allowed-tools: []
---
test prompt`);
    // Default walker behaviour: not invoked (primary succeeds). Tests that
    // need walker behaviour override per-test.
    walkAdapterFallbackMock.mockReturnValue({
      winner: null,
      hopsAttempted: ["lm-studio"],
      lastError: "lm-studio model not configured",
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("emits per-hop log lines in the AC #4 format when walker tries fallback", () => {
    walkAdapterFallbackMock.mockReturnValue({
      winner: { adapter: "claude", source: "fallback" },
      hopsAttempted: ["lm-studio", "codex", "claude"],
      lastError: "codex broken",
    });

    const stderrLines: string[] = [];
    runStageSkillHeadless("feature-dev", 42, {
      onStderr: (line: string) => stderrLines.push(line),
    });

    // The walker was called and the success-path proceeded — so no error
    // envelope. The per-hop log lines are emitted via onStderr in the
    // AC-specified format. One line per fallback candidate (skipping
    // hopsAttempted[0] which is the failed primary).
    const log = stderrLines.join("");
    expect(log).toMatch(
      /\[skillRunner\] primary=lm-studio unavailable: [\s\S]*?; falling back to codex per pipeline\.adapter_fallback_chain/
    );
    expect(log).toMatch(
      /\[skillRunner\] primary=lm-studio unavailable: [\s\S]*?; falling back to claude per pipeline\.adapter_fallback_chain/
    );
  });

  it("propagates adapterFallbackChainUsed on success when fallback walked", () => {
    walkAdapterFallbackMock.mockReturnValue({
      winner: { adapter: "codex", source: "fallback" },
      hopsAttempted: ["lm-studio", "codex"],
      lastError: "lm-studio model not configured",
    });

    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: expect.objectContaining({
          adapter: "codex",
          source: "fallback",
          adapterFallbackChainUsed: ["lm-studio", "codex"],
        }),
      })
    );
  });

  it("emits [stage:no-adapter-available] when full chain is exhausted (AC #5)", () => {
    walkAdapterFallbackMock.mockReturnValue({
      winner: null,
      hopsAttempted: ["lm-studio", "codex", "gemini"],
      lastError: "every adapter unavailable",
    });

    const onError = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    expect(onError).toHaveBeenCalled();
    const errArg = onError.mock.calls[0][0] as Error;
    expect(errArg.message).toMatch(/^\[stage:no-adapter-available\]/);
    expect(errArg.message).toContain("adapters_tried=[lm-studio,codex,gemini]");
    expect(errArg.message).toContain("reason=");

    // The audit trail rides through to onComplete so HeadlessOrchestrator
    // can persist it onto the failed-stage history record.
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        adapterDecision: expect.objectContaining({
          adapterFallbackChainUsed: ["lm-studio", "codex", "gemini"],
        }),
      })
    );
  });

  it("emits [stage:adapter-unavailable] when walker returned empty chain (strict-mode / no fallback)", () => {
    // disable_fallback: true, or empty effective chain — walker returns
    // hopsAttempted=[primary] only and null winner. The dispatcher must
    // emit the older [stage:adapter-unavailable] envelope, NOT the
    // chain-exhausted one.
    walkAdapterFallbackMock.mockReturnValue({
      winner: null,
      hopsAttempted: ["lm-studio"],
      lastError: "lm-studio model not configured",
    });

    const onError = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onError });

    expect(onError).toHaveBeenCalled();
    const errArg = onError.mock.calls[0][0] as Error;
    expect(errArg.message).toMatch(/^\[stage:adapter-unavailable\]/);
    expect(errArg.message).toContain("adapter=lm-studio");
    expect(errArg.message).not.toContain("adapters_tried=");
  });
});

/**
 * Cap-recovery adapter pin (Issue #1545).
 *
 * A SECOND trigger reaches `pipeline.adapter_fallback_chain`, and it is walked
 * somewhere else entirely. The walker above is this layer's own, strictly
 * stage-start, and fires only when `validateAdapterPrerequisites` fails — a
 * missing CLI or a logged-out session, both knowable before any token is spent.
 * A usage cap is knowable only mid-run, after the stage is already lost, so the
 * Go scheduler owns that walk: it is the only component that can tell the tier
 * ladder is spent. Widening the walker above to cover both would have handed a
 * mid-stream PREREQ failure permission to re-run a stage that had already spent
 * tokens, which is exactly the waste its stage-start bound exists to prevent.
 *
 * What arrives here is therefore not a decision to make but one already made:
 * `adapterPin`, the last positional argument, naming the provider this dispatch
 * must land on. These tests pin that it outranks local resolution, that it is
 * validated rather than trusted, and that its absence changes nothing.
 */
describe("skillRunner — cap-recovery adapter pin (Issue #1545)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
description: test
allowed-tools: []
---
test prompt`);
    walkAdapterFallbackMock.mockReturnValue({
      winner: null,
      hopsAttempted: [],
      lastError: "",
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  /** runStageSkillHeadless takes 17 positional arguments; the pin is the last. */
  const runWithPin = (pin: string | undefined, onComplete: (r: unknown) => void) =>
    runStageSkillHeadless(
      "feature-dev",
      42,
      { onComplete },
      undefined, // issueMetadata
      undefined, // _batchContext
      undefined, // skipToPhase
      undefined, // modelOverride
      undefined, // pauseAutoRouting
      undefined, // pinnedWorkspaceRoot
      undefined, // modelOverrideSource
      undefined, // injectedSkillContent
      undefined, // autonomousMode
      undefined, // warnThresholdUsd
      undefined, // targetRepoOverride
      undefined, // runId
      undefined, // effortOverride
      pin
    );

  it("runs the stage on the pinned adapter instead of the configured one", () => {
    // The configured answer is the provider whose cap just cost this run a
    // stage. Honouring it would walk the recovery straight back into the wall.
    resolveStageAdapterMock.mockReturnValue({ adapter: "claude", source: "stage-config" });

    const onComplete = vi.fn();
    runWithPin("codex", onComplete);
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: expect.objectContaining({ adapter: "codex", source: "cap-fallback" }),
      })
    );
  });

  it("records the pin as cap-fallback, distinct from the prereq walker's own fallback", () => {
    // Two triggers, two sources. Collapsing them would make the audit trail
    // unable to say whether a stage moved because a CLI was missing or because
    // a provider stopped serving the account.
    resolveStageAdapterMock.mockReturnValue({ adapter: "claude", source: "stage-config" });

    const onComplete = vi.fn();
    runWithPin("grok", onComplete);
    mockProcess.emit("close", 0);

    const decision = onComplete.mock.calls[0]?.[0]?.adapterDecision;
    expect(decision.source).toBe("cap-fallback");
    expect(decision.source).not.toBe("fallback");
  });

  it("ignores a pin the adapter enum does not recognise", () => {
    // A typo in a pin must not fail the stage outright — the local decision is
    // still a working answer, and losing a stage to a bad string would be a
    // worse outcome than the cap the pin was recovering from.
    resolveStageAdapterMock.mockReturnValue({ adapter: "claude", source: "stage-config" });

    const onComplete = vi.fn();
    runWithPin("clawed", onComplete);
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: expect.objectContaining({ adapter: "claude", source: "stage-config" }),
      })
    );
  });

  it("changes nothing when no pin is sent — the ordinary dispatch", () => {
    // #611's rule that this layer owns per-stage adapter selection is intact
    // wherever Go says nothing, and Go says nothing on every dispatch but a
    // cap recovery.
    resolveStageAdapterMock.mockReturnValue({ adapter: "claude", source: "auto-router" });

    const onComplete = vi.fn();
    runWithPin(undefined, onComplete);
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: expect.objectContaining({ adapter: "claude", source: "auto-router" }),
      })
    );
  });
});
