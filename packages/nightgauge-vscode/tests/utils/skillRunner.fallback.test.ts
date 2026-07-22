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

vi.mock("child_process", () => ({
  spawn: vi.fn(),
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
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: true,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
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
