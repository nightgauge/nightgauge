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
    // Adapter-specific config getters (fallback path)
    getGeminiModel: vi.fn(() => "gemini-2.0-flash"),
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
    if (filePath.includes("run-stage.sh")) return true;
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
  vi.mocked(getGeminiModel).mockReturnValue("gemini-2.0-flash");
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

  // Issue #19: efficiency is now an envelope (no per-stage pin), so — like
  // elevated — it falls through to the configured adapter model rather than a
  // translated tier. Only Maximum (still pinned) stamps a translated id.
  it("efficiency (envelope) falls through to getGeminiModel — no pin translation", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.0-flash");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.0-flash");
  });

  it("maximum maps feature-dev → gemini-2.5-pro via NIGHTGAUGE_GEMINI_MODEL", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
  });

  it("elevated falls through to getGeminiModel — no override applied", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.5-pro");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.5-pro");
  });

  it("explicit pipeline.stage_models keeps precedence — no perf-mode mapping", () => {
    // When stage_models is set under elevated mode, source = "config" and the
    // adapter dispatch must NOT translate it via the perf-mode table.
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getStageModel).mockReturnValue("haiku");
    vi.mocked(getGeminiModel).mockReturnValue("gemini-2.0-flash");

    runStageSkillHeadless("feature-dev", 42, {});

    // The configured fallback is used; the alias "haiku" is not leaked.
    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBe("gemini-2.0-flash");
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
  // rejects it as primary and the fallback walker lands on an agentic
  // adapter — the gemini-sdk model env is never stamped. Performance-mode
  // wiring for it died with the gate (previously asserted here).
  it("never dispatches gemini-sdk: fallback adapter spawns without NIGHTGAUGE_GEMINI_MODEL", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("pr-create", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_GEMINI_MODEL).toBeUndefined();
  });
});

describe("copilot adapter — performance-mode wiring (Issue #3214)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "copilot";
    setExistsForAdapter("copilot");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  it("efficiency (envelope) falls through to getCopilotModel — no pin translation", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("configured-copilot-model");
  });

  it("maximum maps feature-dev → claude-sonnet-4.5", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("claude-sonnet-4.5");
  });

  it("efficiency (envelope) falls through to getCopilotModel for issue-pickup", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("efficiency");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("issue-pickup", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("configured-copilot-model");
  });

  it("elevated falls through to getCopilotModel — no override applied", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("elevated");
    vi.mocked(getCopilotModel).mockReturnValue("configured-copilot-model");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_COPILOT_MODEL).toBe("configured-copilot-model");
  });
});

describe("lm-studio adapter — agentic gate bars pipeline dispatch (#57)", () => {
  beforeEach(() => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "lm-studio";
    setExistsForAdapter("lm-studio");
    vi.mocked(spawn).mockReturnValue(createMockChildProcess());
  });

  // lm-studio is chat-completion-only: the agentic gate rejects it as
  // primary and the fallback walker lands on an agentic adapter — the
  // local-model env is never stamped and the perf-mode mismatch warning
  // path is unreachable (previously asserted here).
  it("never dispatches lm-studio: fallback adapter spawns without the local model env", () => {
    vi.mocked(getPerformanceMode).mockReturnValue("maximum");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(lastSpawnEnv().NIGHTGAUGE_LM_STUDIO_MODEL).toBeUndefined();
  });
});
