/**
 * skillRunner.adapterPerfMode.test.ts (Issue #3214)
 *
 * Verifies the adapter dispatch in skillRunner honors the performance-mode
 * profile for every non-Claude adapter:
 *   - gemini / gemini-sdk → NIGHTGAUGE_GEMINI_MODEL stamped to mapped id.
 *   - copilot             → NIGHTGAUGE_COPILOT_MODEL stamped to mapped id.
 *   - lm-studio           → keeps configured local model and demotes
 *                           modelDecision.source to "config" via the warning
 *                           path (the spawn env keeps the configured local
 *                           model untouched).
 *
 * Mirrors the mock plumbing in skillRunner.copilot.test.ts.
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
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

// Partial mock: keep getModeStageAdapterModel/MODE_PROFILES real so the
// translation tables exercise actual code; override the runtime getters.
vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getExecutionAdapter: vi.fn((): string => {
      const env = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      if (
        env === "gemini" ||
        env === "gemini-sdk" ||
        env === "copilot" ||
        env === "codex" ||
        env === "lm-studio"
      ) {
        return env;
      }
      return "claude";
    }),
    getDefaultModel: vi.fn(() => undefined),
    getStageModel: vi.fn(() => undefined),
    getStageEffort: vi.fn(() => "medium"),
    getStageOverrideModel: vi.fn(() => undefined),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getStageMcpTools: vi.fn(() => []),
    getMcpToolsConfig: vi.fn(() => []),
    getModelRoutingMode: vi.fn(() => "automatic"),
    getLargeDiffThreshold: vi.fn(() => 500),
    getExperimentConfig: vi.fn(() => undefined),
    getConfidenceThreshold: vi.fn(() => 0.5),
    getMinimumModel: vi.fn(() => undefined),
    getStageModelsMatrix: vi.fn(() => undefined),
    getTypeOverrides: vi.fn(() => undefined),
    // Performance-mode getters — the focus of these tests
    getPerformanceMode: vi.fn(() => "elevated"),
    // Adapter-specific config getters (fallback path).
    //
    // The configured fallback must be a NON-DEPRECATED registry id: the gemini
    // adapter is a closed model set derived from the registry's live
    // `provider: "google"` entries, so a deprecated id (gemini-2.0-flash, which
    // Google shut down 2026-06-01) fails preflight and the stage never spawns.
    // It must also DIFFER from the id a tier alias would translate to
    // (haiku/sonnet → gemini-2.5-flash), or the "no translation happened"
    // assertions below would pass whether or not translation occurred.
    getGeminiModel: vi.fn(() => "gemini-2.5-pro"),
    getGeminiAuthMethod: vi.fn(() => "api-key"),
    getCopilotModel: vi.fn(() => "configured-copilot-model"),
    getLmStudioModel: vi.fn(() => "local-llama-3.1"),
    getLmStudioBaseUrl: vi.fn(() => "http://127.0.0.1:1234/v1"),
    getLmStudioApiKey: vi.fn(() => ""),
    getLmStudioTimeoutMs: vi.fn(() => 60_000),
    // Codex getters not under test here, but skillRunner imports them
    resolveCodexPipelineModel: vi.fn((alias: string) => {
      if (alias === "haiku") return "gpt-5.4-mini";
      if (alias === "sonnet") return "gpt-5.4";
      if (alias === "opus") return "gpt-5.5";
      return alias;
    }),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => ""),
    getCodexResumeEnabled: vi.fn(() => false),
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

import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import {
  getPerformanceMode,
  getCopilotModel,
  getStageModel,
  getGeminiModel,
} from "../../src/utils/incrediConfig";

const MOCK_SKILL_CONTENT = `---
name: test-skill
allowed-tools: Read Write Edit
---
# Test Skill

Test content.
`;

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

function setExistsForAdapter(adapter: "gemini" | "gemini-sdk" | "copilot" | "lm-studio") {
  vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
    const filePath = String(p);
    if (filePath.includes("SKILL.md") || filePath.includes("skills/")) return true;
    if (filePath.includes("sdk-cli.cjs")) return true;
    if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) return true;
    if (
      filePath.endsWith("/node") ||
      filePath.endsWith("/git") ||
      filePath.endsWith("/gh") ||
      filePath.endsWith("/copilot") ||
      filePath.endsWith("/gemini") ||
      filePath.endsWith(`/${adapter}`)
    ) {
      return true;
    }
    return false;
  });
  vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
}

function lastSpawnEnv(): Record<string, string> {
  const calls = vi.mocked(spawn).mock.calls;
  expect(calls.length, "spawn was not called").toBeGreaterThan(0);
  const lastCall = calls[calls.length - 1];
  const opts = lastCall[2] as { env?: Record<string, string> };
  return opts?.env ?? {};
}

const originalEnv = process.env;

beforeEach(() => {
  vi.clearAllMocks();
  process.env = {
    ...originalEnv,
    PATH: "/usr/local/bin:/usr/bin:/bin",
    VITEST: "true",
  };
  // Reset getter defaults each test
  vi.mocked(getPerformanceMode).mockReturnValue("elevated");
  vi.mocked(getStageModel).mockReturnValue(undefined);
  vi.mocked(getGeminiModel).mockReturnValue("gemini-2.5-pro");
  vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");
});

afterEach(() => {
  process.env = {
    ...originalEnv,
    VITEST: "true",
  };
  vi.restoreAllMocks();
});

describe("gemini adapter — performance-mode wiring (Issue #3214)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini";
    setExistsForAdapter("gemini");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  it("efficiency translates its resolved band for Gemini", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.5-pro");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-flash");
  });

  it("maximum maps feature-dev → gemini-2.5-pro via NIGHTGAUGE_GEMINI_MODEL", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
  });

  it("elevated translates its resolved band for Gemini", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.5-pro");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-flash");
  });

  it("translates an explicit pipeline.stage_models band", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getStageModel).mockReturnValue("haiku");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.5-pro");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-flash");
  });

  it("repeated dispatches quiesce operator diagnostics before Vitest teardown", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    vi.mocked(spawn).mockImplementation(() => createMockChildProcess());
    const consoleSpy = vi.spyOn(console, "log").mockImplementation(() => undefined);
    const iterations = 25;

    for (let index = 0; index < iterations; index += 1) {
      runStageSkillHeadless("feature-dev", 42, {});
    }

    expect(spawn).toHaveBeenCalledTimes(iterations);
    for (const call of vi.mocked(spawn).mock.calls) {
      const opts = call[2] as { env?: Record<string, string> };
      expect(opts.env?.NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
    }
    expect(consoleSpy).not.toHaveBeenCalled();
  });
});

describe("gemini-sdk adapter — agentic gate bars pipeline dispatch (#57)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "gemini-sdk";
    setExistsForAdapter("gemini-sdk");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  // gemini-sdk is chat-completion-only (no tool loop): the agentic gate
  // Explicit provider selections fail closed unless the user configures a
  // fallback chain. A chat-only adapter must not silently become billable
  // work on another provider.
  it("fails closed without silently dispatching another provider", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    const onError = vi.fn();

    runStageSkillHeadless("pr-create", 42, { onError });

    expect(spawn).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining("chat-completion-only") })
    );
  });
});

describe("copilot adapter — performance-mode wiring (Issue #3214)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "copilot";
    setExistsForAdapter("copilot");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  it("efficiency translates its resolved band for Copilot", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("gpt-4o");
  });

  it("maximum maps feature-dev → claude-sonnet-4.5", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("claude-sonnet-4.5");
  });

  it("efficiency translates the issue-pickup band for Copilot", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("issue-pickup", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("gpt-4o-mini");
  });

  it("elevated translates its resolved band for Copilot", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("gpt-4o");
  });
});

describe("lm-studio adapter — agentic gate bars pipeline dispatch (#57)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "lm-studio";
    setExistsForAdapter("lm-studio");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  // lm-studio is chat-completion-only: the agentic gate rejects it as
  // primary and fails closed unless the user explicitly configured fallback.
  it("fails closed without silently dispatching another provider", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");
    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    expect(spawn).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining("chat-completion-only") })
    );
  });
});
