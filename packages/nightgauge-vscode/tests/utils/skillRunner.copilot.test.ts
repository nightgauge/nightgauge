/**
 * skillRunner.copilot.test.ts
 *
 * Unit tests for Copilot adapter support added in issue #1946:
 * - getCopilotModel() getter in incrediConfig.ts
 * - validateAdapterPrerequisites() Copilot branch in skillRunner.ts
 * - copilotEnv block propagated to spawn env
 *
 * @see Issue #1946 - Add Copilot CLI execution branch in skillRunner
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

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

// Partial mock: keep real implementations for most getters; override only
// getExecutionAdapter so we can switch to 'copilot' via env var.
vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getExecutionAdapter: vi.fn((): string => {
      const env = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      if (env === "copilot") return "copilot";
      if (env === "codex") return "codex";
      return "claude";
    }),
    getDefaultModel: vi.fn(() => undefined),
    getStageModel: vi.fn(() => undefined),
    getStageEffort: vi.fn(() => undefined),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getStageMcpTools: vi.fn(() => []),
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

// Issue #3231: when an adapter's prereq fails, the dispatcher walks
// `pipeline.adapter_fallback_chain`. These tests assert the *primary-only*
// failure shape (`[stage:adapter-unavailable]`), so we pin the walker to
// return primary-only — equivalent to strict mode (`disable_fallback: true`)
// — to preserve the original assertions while leaving the prereq probe paths
// under test.
vi.mock("../../src/utils/resolvers/adapterResolver", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/resolvers/adapterResolver")>(
    "../../src/utils/resolvers/adapterResolver"
  );
  return {
    ...actual,
    walkAdapterFallback: vi.fn((primary: string, lastError: string | null) => ({
      winner: null,
      hopsAttempted: [primary],
      lastError: lastError ?? null,
    })),
  };
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

/** Default skill file content returned by readFileSync mock. */
const MOCK_SKILL_CONTENT = `---
name: test-skill
allowed-tools: Read Write Edit
---
# Test Skill

Test content.
`;

/** Set up fs mocks so the skill file is found and readable. */
function mockSkillFileExists() {
  vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
    const filePath = String(p);
    // Skill file paths contain "skills/" or "SKILL.md"
    if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
      return true;
    }
    return false;
  });
  vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
}

// ---------------------------------------------------------------------------
// Imports (after mocks)
// ---------------------------------------------------------------------------

import { runStageSkillHeadless, runStageSkillInteractive } from "../../src/utils/skillRunner";
import { getCopilotModel } from "../../src/utils/incrediConfig";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";

// ---------------------------------------------------------------------------
// getCopilotModel — unit tests
// ---------------------------------------------------------------------------

describe("getCopilotModel", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_COPILOT_MODEL;

    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns NIGHTGAUGE_COPILOT_MODEL env var when set", () => {
    process.env.NIGHTGAUGE_COPILOT_MODEL = "claude-3.5-sonnet";
    expect(getCopilotModel("/test/workspace")).toBe("claude-3.5-sonnet");
  });

  it("returns empty string when no config file exists", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    expect(getCopilotModel("/test/workspace")).toBe("");
  });

  it("returns model from YAML config under ui.core.copilot.model", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`ui:
  core:
    copilot:
      model: gpt-4o
`);
    expect(getCopilotModel("/test/workspace")).toBe("gpt-4o");
  });

  it("returns empty string when config has no model under copilot", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`ui:
  core:
    copilot: {}
`);
    expect(getCopilotModel("/test/workspace")).toBe("");
  });

  it("returns empty string when config has no copilot section", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`ui:
  core:
    adapter: claude
`);
    expect(getCopilotModel("/test/workspace")).toBe("");
  });

  it("env var takes priority over YAML config", () => {
    process.env.NIGHTGAUGE_COPILOT_MODEL = "env-model";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`ui:
  core:
    copilot:
      model: config-model
`);
    expect(getCopilotModel("/test/workspace")).toBe("env-model");
  });
});

// ---------------------------------------------------------------------------
// validateAdapterPrerequisites — Copilot branch (via runStageSkillHeadless)
// ---------------------------------------------------------------------------

describe("validateAdapterPrerequisites — copilot", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = {
      ...originalEnv,
      NIGHTGAUGE_UI_CORE_ADAPTER: "copilot",
      PATH: "/usr/local/bin:/usr/bin:/bin",
    };
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns headless-only error when copilot adapter used in interactive mode", () => {
    // Skill file must exist so the check reaches adapter validation
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);

    const onError = vi.fn();
    const onComplete = vi.fn();

    runStageSkillInteractive("feature-dev", 42, { onError, onComplete });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("headless execution only"),
      })
    );
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({ success: false, exitCode: null })
    );
  });

  it("returns error when run-stage.sh is not found", () => {
    // Skill file exists but run-stage.sh does not
    vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
      const filePath = String(p);
      if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
        return true;
      }
      if (filePath.includes("run-stage.sh")) return false;
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);

    const onError = vi.fn();
    const onComplete = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("Unified stage runner not found"),
      })
    );
  });

  it("returns error when copilot binary is not in PATH", () => {
    // commandExists() has a VITEST bypass (process.env.VITEST === 'true' → true).
    // Temporarily disable so the real PATH-based logic runs.
    const savedVitest = process.env.VITEST;
    process.env.VITEST = "false";

    vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
      const filePath = String(p);
      if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
        return true;
      }
      if (filePath.includes("run-stage.sh")) return true;
      if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) {
        return true;
      }
      // node, git, gh → true; copilot → false (not installed)
      if (filePath.endsWith("/node") || filePath.endsWith("/git") || filePath.endsWith("/gh")) {
        return true;
      }
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);

    const onError = vi.fn();
    const onComplete = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    process.env.VITEST = savedVitest;

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("`copilot` CLI is not available"),
      })
    );
  });

  it("returns error when SDK CLI is not built", () => {
    vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
      const filePath = String(p);
      if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
        return true;
      }
      if (filePath.includes("run-stage.sh")) return true;
      // node, git, gh, copilot → true; SDK CLI → false
      if (
        filePath.endsWith("/node") ||
        filePath.endsWith("/git") ||
        filePath.endsWith("/gh") ||
        filePath.endsWith("/copilot")
      ) {
        return true;
      }
      if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) {
        return false;
      }
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);

    const onError = vi.fn();
    const onComplete = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("built Nightgauge SDK CLI"),
      })
    );
  });

  it("spawns process when all prerequisites are met", () => {
    vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
      const filePath = String(p);
      if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
        return true;
      }
      if (filePath.includes("run-stage.sh")) return true;
      if (
        filePath.endsWith("/node") ||
        filePath.endsWith("/git") ||
        filePath.endsWith("/gh") ||
        filePath.endsWith("/copilot")
      ) {
        return true;
      }
      if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) {
        return true;
      }
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onError = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onError });

    expect(onError).not.toHaveBeenCalled();
    expect(spawn).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// copilotEnv — spawn env propagation
// ---------------------------------------------------------------------------

describe("copilotEnv — spawn env", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = {
      ...originalEnv,
      NIGHTGAUGE_UI_CORE_ADAPTER: "copilot",
      PATH: "/usr/local/bin:/usr/bin:/bin",
    };
    delete process.env.NIGHTGAUGE_COPILOT_MODEL;
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;

    // All prerequisite checks pass
    vi.mocked(fs.existsSync).mockImplementation((p: unknown) => {
      const filePath = String(p);
      if (filePath.includes("SKILL.md") || filePath.includes("skills/")) {
        return true;
      }
      if (filePath.includes("run-stage.sh")) return true;
      if (
        filePath.endsWith("/node") ||
        filePath.endsWith("/git") ||
        filePath.endsWith("/gh") ||
        filePath.endsWith("/copilot")
      ) {
        return true;
      }
      if (filePath.includes("nightgauge-sdk/dist/cli/index.js")) {
        return true;
      }
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(MOCK_SKILL_CONTENT);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("passes NIGHTGAUGE_COPILOT_MODEL when env var is set", () => {
    process.env.NIGHTGAUGE_COPILOT_MODEL = "gpt-4o";

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          NIGHTGAUGE_COPILOT_MODEL: "gpt-4o",
        }),
      })
    );
  });

  it("does not set NIGHTGAUGE_COPILOT_MODEL when not configured", () => {
    // No env var set and no config file
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.not.objectContaining({
          NIGHTGAUGE_COPILOT_MODEL: expect.any(String),
        }),
      })
    );
  });

  it("passes GH_TOKEN when present in process env", () => {
    process.env.GH_TOKEN = "ghp_test_token";

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          GH_TOKEN: "ghp_test_token",
        }),
      })
    );
  });

  it("passes GITHUB_TOKEN when present in process env", () => {
    process.env.GITHUB_TOKEN = "github_token_value";

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          GITHUB_TOKEN: "github_token_value",
        }),
      })
    );
  });

  it("passes COPILOT_GITHUB_TOKEN when present in process env", () => {
    process.env.COPILOT_GITHUB_TOKEN = "copilot_token";

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          COPILOT_GITHUB_TOKEN: "copilot_token",
        }),
      })
    );
  });

  it("sets NIGHTGAUGE_ADAPTER to copilot in spawn env", () => {
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          NIGHTGAUGE_ADAPTER: "copilot",
          NIGHTGAUGE_OUTPUT_FORMAT: "json",
        }),
      })
    );
  });
});
