/**
 * skillRunner.mixed-adapter.test.ts
 *
 * Smoke test for Issue #3223 — mixed-adapter pipeline dispatch.
 *
 * Exercises a 2-stage pipeline (planning=claude, feature-dev=gemini) and
 * asserts that:
 *
 *   1. Each stage calls `resolveStageAdapter(stage)` (per-stage dispatch),
 *      not the prior global lookup.
 *   2. Stage A (claude) spawn args match the Claude CLI shape (`claude -p ...`).
 *   3. Stage B (gemini) spawn args match `scripts/run-stage.sh gemini ...`.
 *   4. MCP tool resolution runs fresh per stage (no leakage across the
 *      adapter switch — `_perStageMcpTools` regression check).
 *   5. onComplete reports `adapterDecision` with `adapter` + `source` per stage.
 *
 * @see Issue #3223 — SkillRunner dispatcher: honor per-stage adapter
 * @see Issue #3221 — B2 resolveStageAdapter resolver
 * @see Issue #3212 — Epic: per-stage adapter selection
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
    createTerminal: vi.fn(() => ({
      show: vi.fn(),
      sendText: vi.fn(),
    })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: {
    getExtension: vi.fn(() => null),
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
  mkdtempSync: vi.fn(() => "/tmp/mock"),
  rmSync: vi.fn(),
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
  // execFileSync is what commandExists() uses. Returning successfully signals
  // the binary is in PATH so prereq checks pass for both claude and gemini.
  execFileSync: vi.fn(() => Buffer.from("/usr/local/bin/cli")),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: true,
  })),
  logDeprecationWarning: vi.fn(),
}));

// Mock incrediConfig — non-MCP getters return defaults, MCP-related getters
// are the focus of the leakage-regression assertion below.
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
    resolveCodexPipelineModel: vi.fn((m?: string) => m ?? "gpt-5.4"),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => undefined),
    getCodexResumeEnabled: vi.fn(() => false),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getGeminiModel: vi.fn(() => "gemini-2.5-pro"),
    getGeminiAuthMethod: vi.fn(() => "api-key"),
    // The leakage-regression check tracks each call to `getStageMcpTools` so
    // the test can assert it was invoked separately for each stage rather
    // than memoized at module load.
    getStageMcpTools: vi.fn(() => []),
    getMcpToolsConfig: vi.fn(() => []),
    // Token resolution stubs
    getGitHubAuthToken: vi.fn(() => null),
    getGitHubAuthTokens: vi.fn(() => ({})),
    getGitHubUser: vi.fn(() => null),
  };
});

// Mock the resolver itself so the test pins per-stage call behavior. The
// resolver is the contract this issue introduces; mocking it lets us assert
// it is invoked once per stage with the correct stage argument.
vi.mock("../../src/utils/resolvers/adapterResolver", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/resolvers/adapterResolver")>(
    "../../src/utils/resolvers/adapterResolver"
  );
  return {
    ...actual,
    resolveStageAdapter: vi.fn((stage: string) => {
      // Mixed-adapter pipeline: planning routes to claude, feature-dev routes
      // to gemini. Mirrors the `pipeline.stage_adapters` block:
      //   feature-planning: claude
      //   feature-dev:      gemini
      if (stage === "feature-planning") {
        return { adapter: "claude", source: "stage-config" };
      }
      if (stage === "feature-dev") {
        return { adapter: "gemini", source: "stage-config" };
      }
      return { adapter: "claude", source: "default" };
    }),
    tryAdapterFallback: vi.fn(() => null),
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

import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { resolveStageAdapter } from "../../src/utils/resolvers/adapterResolver";
import { getStageMcpTools, getMcpToolsConfig } from "../../src/utils/incrediConfig";

describe("Mixed-adapter pipeline (Issue #3223)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("AC #1 — calls resolveStageAdapter(stage) per stage, not the global lookup", () => {
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
    runStageSkillHeadless("feature-planning", 42, {});
    runStageSkillHeadless("feature-dev", 42, {});

    expect(resolveStageAdapter).toHaveBeenCalledTimes(2);
    // Issue #3230 added env + optional autoRouterOptions trailing args.
    // Assert the two leading positional args and ignore the rest.
    const [firstCall, secondCall] = vi.mocked(resolveStageAdapter).mock.calls;
    expect(firstCall[0]).toBe("feature-planning");
    expect(firstCall[1]).toBe("/test/workspace");
    expect(secondCall[0]).toBe("feature-dev");
    expect(secondCall[1]).toBe("/test/workspace");
  });

  it("AC #2 — claude stage spawns Claude CLI with -p shape", () => {
    const proc = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(proc);
    runStageSkillHeadless("feature-planning", 42, {});

    const [cmd, args] = vi.mocked(spawn).mock.calls[0];
    expect(cmd).toBe("claude");
    expect(args).toEqual(
      expect.arrayContaining(["-p", "--output-format", "stream-json", "--allowedTools"])
    );
  });

  it("AC #3 — gemini stage spawns scripts/run-stage.sh with gemini and stage args", () => {
    const proc = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(proc);
    runStageSkillHeadless("feature-dev", 42, {});

    const [cmd, args] = vi.mocked(spawn).mock.calls[0];
    expect(String(cmd)).toContain("scripts/run-stage.sh");
    expect(args).toEqual(["gemini", "feature-dev", "42"]);
  });

  it("AC #3 — gemini stage env carries NIGHTGAUGE_ADAPTER=gemini and gemini config", () => {
    const proc = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(proc);
    runStageSkillHeadless("feature-dev", 42, {});

    const opts = vi.mocked(spawn).mock.calls[0][2] as { env: Record<string, string> };
    expect(opts.env).toEqual(
      expect.objectContaining({
        NIGHTGAUGE_ADAPTER: "gemini",
        NIGHTGAUGE_OUTPUT_FORMAT: "json",
        NIGHTGAUGE_GEMINI_MODEL: "gemini-2.5-pro",
      })
    );
  });

  it("AC #4 / technical_notes #2 — MCP tool resolution runs fresh per stage", () => {
    // Critical regression check: when the dispatcher switches adapter mid-
    // pipeline, the MCP allowlist must be recomputed for the new stage rather
    // than reusing whatever was resolved for the prior one. The skill calls
    // getStageMcpTools(workspaceRoot, stage) inside the dispatcher (not at
    // module load), so we expect one invocation per stage with the stage name.
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
    runStageSkillHeadless("feature-planning", 42, {});
    runStageSkillHeadless("feature-dev", 42, {});

    expect(getStageMcpTools).toHaveBeenCalledTimes(2);
    expect(getStageMcpTools).toHaveBeenNthCalledWith(1, "/test/workspace", "feature-planning");
    expect(getStageMcpTools).toHaveBeenNthCalledWith(2, "/test/workspace", "feature-dev");

    // Same pattern for getMcpToolsConfig — it is also stage-scoped.
    expect(getMcpToolsConfig).toHaveBeenCalledTimes(2);
    expect(getMcpToolsConfig).toHaveBeenNthCalledWith(1, "/test/workspace", "feature-planning");
    expect(getMcpToolsConfig).toHaveBeenNthCalledWith(2, "/test/workspace", "feature-dev");
  });

  it("AC #5 — onComplete reports adapter + adapter_source per stage", () => {
    const planProc = createMockChildProcess();
    const devProc = createMockChildProcess();
    vi.mocked(spawn).mockReturnValueOnce(planProc).mockReturnValueOnce(devProc);

    const planOnComplete = vi.fn();
    const devOnComplete = vi.fn();
    runStageSkillHeadless("feature-planning", 42, { onComplete: planOnComplete });
    runStageSkillHeadless("feature-dev", 42, { onComplete: devOnComplete });

    planProc.emit("close", 0);
    devProc.emit("close", 0);

    expect(planOnComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "claude", source: "stage-config" },
      })
    );
    expect(devOnComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "gemini", source: "stage-config" },
      })
    );
  });
});
