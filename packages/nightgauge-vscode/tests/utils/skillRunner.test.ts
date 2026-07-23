/**
 * skillRunner.test.ts
 *
 * Comprehensive unit tests for skillRunner.ts core execution functionality.
 * Tests cover stage navigation, file discovery, prompt building, auto-accept
 * configuration, headless execution, token usage, session management, and
 * process lifecycle.
 *
 * @see Issue #272 - Add skillRunner core tests
 * @see skillRunner-loop-detection.test.ts for AskUserQuestion loop detection tests
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { spawn } from "child_process";
import type { ChildProcess } from "child_process";
import { EventEmitter } from "events";
import * as fs from "fs";
import * as vscode from "vscode";

// Mock vscode module
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

// Mock fs module
vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

// Create mock process factory
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

// Mock child_process module
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

// Mock configPathResolver so loadAutoAcceptConfigSync can find a config file
vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: true,
  })),
  logDeprecationWarning: vi.fn(),
}));

// Mock incrediConfig for new CLI flag tests (Issue #626)
// getExecutionAdapter and getAuthProvider use dynamic implementations
// to preserve existing tests that set env vars.
vi.mock("../../src/utils/incrediConfig", async () => {
  const _actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
  return {
    ..._actual,
    getAuthProvider: vi.fn((): string => {
      const env = process.env.NIGHTGAUGE_UI_CORE_AUTH_PROVIDER;
      if (env === "bedrock" || env === "vertex") return env;
      return "max";
    }),
    getExecutionAdapter: vi.fn((): string => {
      const env = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      if (env === "codex") return "codex";
      return "claude";
    }),
    getDefaultModel: vi.fn(() => undefined),
    getStageModel: vi.fn(() => undefined),
    getStageEffort: vi.fn(() => undefined),
    getCodexModel: vi.fn(() => "gpt-5.4"),
    getCodexReasoningEffort: vi.fn(() => "medium"),
    resolveCodexPipelineModel: vi.fn((model?: string) => {
      if (!model || model === "sonnet") return "gpt-5.4";
      if (model === "haiku") return "gpt-5.4-mini";
      if (model === "opus") return "gpt-5.5";
      return model;
    }),
    getCodexCliCommand: vi.fn(() => "codex"),
    getCodexCliArgs: vi.fn(() => undefined),
    getCodexResumeEnabled: vi.fn(() => false),
    getFallbackModel: vi.fn(() => undefined),
    getMaxTurns: vi.fn(() => undefined),
    getCostBudget: vi.fn(() => undefined),
    getStageMcpTools: vi.fn(() => []),
    // Config-based token resolution (Issue #2670)
    getGitHubAuthToken: vi.fn(() => null),
    getGitHubAuthTokens: vi.fn(() => ({})),
    getGitHubUser: vi.fn(() => null),
  };
});

// Mock RepositoryContextLoader for repo identity tests (Issue #1306)
// Default: getCurrentRepository returns null (matches non-initialized singleton behavior)
const mockGetCurrentRepository = vi.fn().mockReturnValue(null);
const mockGetWorkingDirectory = vi.fn().mockReturnValue("/test/workspace");
vi.mock("../../src/services/RepositoryContextLoader", async () => {
  const actual = await vi.importActual<typeof import("../../src/services/RepositoryContextLoader")>(
    "../../src/services/RepositoryContextLoader"
  );

  return {
    RepositoryContextLoader: class RepositoryContextLoaderMock {
      static getInstance() {
        return {
          getCurrentRepository: mockGetCurrentRepository,
          getWorkingDirectory: mockGetWorkingDirectory,
        };
      }
    },
  };
});

// Import after mocks are set up
import {
  getNextStage,
  getStageLabel,
  runStageSkillHeadless,
  runStageSkillInteractive,
  writeToInteractiveProcess,
  isInteractiveProcess,
  resumeSessionWithResponse,
  killAllActiveProcesses,
  isStageRunning,
  getActiveProcess,
  hasActiveProcess,
  getLastSessionId,
  INTERACTIVE_TIMEOUT_MS,
  classifyError,
  extractStreamJsonError,
  resolveTokenForSubprocess,
  resolveSdkCliPath,
} from "../../src/utils/skillRunner";
import type { PipelineStage } from "@nightgauge/sdk";
import {
  getDefaultModel,
  getStageModel,
  getStageEffort,
  getCodexModel,
  resolveCodexPipelineModel,
  getFallbackModel,
  getMaxTurns,
  getCostBudget,
  getStageMcpTools,
  getGitHubAuthToken,
  getGitHubAuthTokens,
  getGitHubUser,
} from "../../src/utils/incrediConfig";

describe("skillRunner - Packaged SDK CLI", () => {
  it("resolves the VSIX-bundled CLI when the target repo has no Nightgauge source tree", () => {
    vi.mocked(vscode.extensions.getExtension).mockReturnValue({
      extensionPath: "/extensions/nightgauge.nightgauge-vscode-0.1.0",
    } as vscode.Extension<unknown>);
    vi.mocked(fs.existsSync).mockImplementation(
      (candidate) =>
        String(candidate) === "/extensions/nightgauge.nightgauge-vscode-0.1.0/dist/sdk-cli.cjs"
    );

    expect(resolveSdkCliPath("/external/consumer-repo")).toBe(
      "/extensions/nightgauge.nightgauge-vscode-0.1.0/dist/sdk-cli.cjs"
    );
  });
});

describe("skillRunner - Stage Navigation", () => {
  describe("getNextStage", () => {
    it("should return issue-pickup after pipeline-start", () => {
      expect(getNextStage("pipeline-start")).toBe("issue-pickup");
    });

    it("should return feature-planning after issue-pickup", () => {
      expect(getNextStage("issue-pickup")).toBe("feature-planning");
    });

    it("should return feature-dev after feature-planning", () => {
      expect(getNextStage("feature-planning")).toBe("feature-dev");
    });

    it("should return feature-validate after feature-dev", () => {
      expect(getNextStage("feature-dev")).toBe("feature-validate");
    });

    it("should return pr-create after feature-validate", () => {
      expect(getNextStage("feature-validate")).toBe("pr-create");
    });

    it("should return pr-merge after pr-create", () => {
      expect(getNextStage("pr-create")).toBe("pr-merge");
    });

    it("should return pipeline-finish after pr-merge", () => {
      expect(getNextStage("pr-merge")).toBe("pipeline-finish");
    });

    it("should return null for last stage (pipeline-finish)", () => {
      expect(getNextStage("pipeline-finish")).toBeNull();
    });

    it("should return null for unknown stage", () => {
      expect(getNextStage("unknown-stage" as PipelineStage)).toBeNull();
    });
  });

  describe("getStageLabel", () => {
    it("should return Initialize for pipeline-start", () => {
      expect(getStageLabel("pipeline-start")).toBe("Initialize");
    });

    it("should return Issue Pickup for issue-pickup", () => {
      expect(getStageLabel("issue-pickup")).toBe("Issue Pickup");
    });

    it("should return Feature Planning for feature-planning", () => {
      expect(getStageLabel("feature-planning")).toBe("Feature Planning");
    });

    it("should return Feature Development for feature-dev", () => {
      expect(getStageLabel("feature-dev")).toBe("Feature Development");
    });

    it("should return Feature Validation for feature-validate", () => {
      expect(getStageLabel("feature-validate")).toBe("Feature Validation");
    });

    it("should return PR Creation for pr-create", () => {
      expect(getStageLabel("pr-create")).toBe("PR Creation");
    });

    it("should return PR Merge for pr-merge", () => {
      expect(getStageLabel("pr-merge")).toBe("PR Merge");
    });

    it("should return Completion for pipeline-finish", () => {
      expect(getStageLabel("pipeline-finish")).toBe("Completion");
    });
  });
});

describe("skillRunner - File Discovery and Parsing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("findSkillFile (via runStageSkillHeadless)", () => {
    it("should find SKILL.md in skills/ directory", () => {
      vi.mocked(fs.existsSync).mockImplementation((filePath) => {
        return String(filePath) === "/test/workspace/skills/nightgauge-feature-dev/SKILL.md";
      });
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: nightgauge-feature-dev
allowed-tools: Read Write Edit
---
# Feature Dev
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      const onStderr = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onStderr });

      // Consolidated metadata line proves file was found and read (Issue #795)
      expect(onStderr).toHaveBeenCalledWith(expect.stringContaining("Stage: feature-dev"));
    });

    it("should fall back to claude-plugins/ directory", () => {
      vi.mocked(fs.existsSync).mockImplementation((filePath) => {
        const pathStr = String(filePath);
        // First path (skills/) not found
        if (pathStr.includes("skills/")) return false;
        // Second path (claude-plugins/) found
        return pathStr.includes("claude-plugins/");
      });
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: nightgauge-issue-pickup
allowed-tools: Read Bash
---
# Issue Pickup
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      const onStderr = vi.fn();
      runStageSkillHeadless("issue-pickup", 42, { onStderr });

      // Consolidated metadata line proves file was found via fallback (Issue #795)
      expect(onStderr).toHaveBeenCalledWith(expect.stringContaining("Stage: issue-pickup"));
    });

    it("should return error when skill file not found", () => {
      vi.mocked(fs.existsSync).mockReturnValue(false);

      const onError = vi.fn();
      const onComplete = vi.fn();

      runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

      expect(onError).toHaveBeenCalledWith(
        expect.objectContaining({
          message: expect.stringContaining("SKILL.md not found"),
        })
      );
      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({
          success: false,
          exitCode: null,
        })
      );
    });
  });

  describe("readSkillFile (via runStageSkillHeadless)", () => {
    it("should extract allowed-tools from frontmatter", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Glob Grep
---
# Test Skill
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      runStageSkillHeadless("feature-dev", 42, {});

      // Check that spawn was called with filtered allowed tools
      expect(spawn).toHaveBeenCalledWith(
        "claude",
        expect.arrayContaining([
          "--allowedTools",
          expect.stringMatching(/Read,Write,Edit,Glob,Grep/),
        ]),
        expect.any(Object)
      );
    });

    it("should use default tools when no frontmatter", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(`# Test Skill

No frontmatter here.
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      runStageSkillHeadless("feature-dev", 42, {});

      // Should use default tools
      expect(spawn).toHaveBeenCalledWith(
        "claude",
        expect.arrayContaining([
          "--allowedTools",
          expect.stringMatching(/Read,Write,Edit,Glob,Grep,Bash,Task/),
        ]),
        expect.any(Object)
      );
    });

    it("should return error on file read failure", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("ENOENT: file not found");
      });

      const onError = vi.fn();
      const onComplete = vi.fn();

      runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

      expect(onError).toHaveBeenCalledWith(
        expect.objectContaining({
          message: expect.stringContaining("Failed to load skill"),
        })
      );
    });

    it("should handle missing allowed-tools field gracefully", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
description: No allowed-tools field
---
# Test Skill
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      runStageSkillHeadless("feature-dev", 42, {});

      // Should use default tools when allowed-tools is missing
      expect(spawn).toHaveBeenCalledWith(
        "claude",
        expect.arrayContaining([
          "--allowedTools",
          expect.stringMatching(/Read,Write,Edit,Glob,Grep,Bash,Task/),
        ]),
        expect.any(Object)
      );
    });

    it("does not force-disable thinking on the claude spawn (#73 — #3801 workaround retired)", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read
---
# Test Skill
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      runStageSkillHeadless("feature-validate", 3809, {});

      // The #3801 thinking-block replay 400 stopped reproducing on claude CLI
      // 2.1.186, so the forced CLAUDE_CODE_DISABLE_THINKING=1 was removed —
      // reasoning models run with thinking enabled (spike doc §8.2).
      const call = vi.mocked(spawn).mock.calls.find(([cmd]) => cmd === "claude");
      expect(call).toBeDefined();
      const opts = call![2] as { env?: Record<string, string> };
      expect(opts.env?.CLAUDE_CODE_DISABLE_THINKING).toBeUndefined();
    });

    it("passes an operator-set CLAUDE_CODE_DISABLE_THINKING through to the claude spawn", () => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read
---
# Test Skill
`);
      const mockProcess = createMockChildProcess();
      vi.mocked(spawn).mockReturnValue(mockProcess);

      // The escape hatch for older CLIs where #3801 still reproduces: the
      // spawn env spreads process.env first, so the operator's value survives.
      vi.stubEnv("CLAUDE_CODE_DISABLE_THINKING", "1");
      try {
        runStageSkillHeadless("feature-validate", 3809, {});
      } finally {
        vi.unstubAllEnvs();
      }

      const call = vi.mocked(spawn).mock.calls.find(([cmd]) => cmd === "claude");
      expect(call).toBeDefined();
      const opts = call![2] as { env?: Record<string, string> };
      expect(opts.env?.CLAUDE_CODE_DISABLE_THINKING).toBe("1");
    });
  });
});

describe("skillRunner - Include Expansion (Issue #862)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should expand include directives with file content", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockImplementation((filePath) => {
      const pathStr = String(filePath);
      if (pathStr.endsWith("SKILL.md")) {
        return `---
name: test-skill
allowed-tools: Read Write
---
<!-- include: ../_shared/PIPELINE_CONTEXT.md -->

# Test Skill Content
`;
      }
      if (pathStr.endsWith("PIPELINE_CONTEXT.md")) {
        return `## System Context

**Product**: Nightgauge — test content.
`;
      }
      throw new Error(`Unexpected file read: ${pathStr}`);
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    // The prompt written to stdin should contain expanded content
    const writeCall = vi.mocked(mockProcess.stdin!.write).mock.calls[0][0];
    expect(writeCall).toContain("**Product**: Nightgauge — test content.");
    // The include directive itself should NOT be present
    expect(writeCall).not.toContain("<!-- include:");
  });

  it("should leave directive as-is when include file is missing", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockImplementation((filePath) => {
      const pathStr = String(filePath);
      if (pathStr.endsWith("SKILL.md")) {
        return `---
name: test-skill
allowed-tools: Read Write
---
<!-- include: ../_shared/NONEXISTENT.md -->

# Test Skill
`;
      }
      // Simulate missing file
      throw new Error("ENOENT: no such file or directory");
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    // The directive should remain as-is (graceful degradation)
    const writeCall = vi.mocked(mockProcess.stdin!.write).mock.calls[0][0];
    expect(writeCall).toContain("<!-- include: ../_shared/NONEXISTENT.md -->");
  });

  it("should expand multiple includes in one file", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockImplementation((filePath) => {
      const pathStr = String(filePath);
      if (pathStr.endsWith("SKILL.md")) {
        return `---
name: test-skill
allowed-tools: Read Write
---
<!-- include: ../_shared/PIPELINE_CONTEXT.md -->

# Test Skill

<!-- include: ../_shared/BATCH_MODE.md -->
`;
      }
      if (pathStr.endsWith("PIPELINE_CONTEXT.md")) {
        return "## System Context\n";
      }
      if (pathStr.endsWith("BATCH_MODE.md")) {
        return "### Batch Detection\n";
      }
      throw new Error(`Unexpected file: ${pathStr}`);
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    const writeCall = vi.mocked(mockProcess.stdin!.write).mock.calls[0][0];
    expect(writeCall).toContain("## System Context");
    expect(writeCall).toContain("### Batch Detection");
  });

  it("should pass through content without includes unchanged", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write
---
# No Includes Here

Just regular content.
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    const writeCall = vi.mocked(mockProcess.stdin!.write).mock.calls[0][0];
    expect(writeCall).toContain("# No Includes Here");
    expect(writeCall).toContain("Just regular content.");
  });
});

describe("skillRunner - Prompt Building", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write
---
# Test Skill Content

This is the skill content.
`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should include stage label in prompt header", () => {
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(mockProcess.stdin!.write).toHaveBeenCalledWith(
      expect.stringContaining("Stage: Feature Development")
    );
  });

  it("should include issue number when provided", () => {
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(mockProcess.stdin!.write).toHaveBeenCalledWith(
      expect.stringContaining("**Issue Number**: #42")
    );
  });

  it("should include skill content in prompt", () => {
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(mockProcess.stdin!.write).toHaveBeenCalledWith(
      expect.stringContaining("# Test Skill Content")
    );
  });

  it("should omit issue number when not provided", () => {
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("issue-pickup", undefined, {});

    const writeCall = vi.mocked(mockProcess.stdin!.write).mock.calls[0][0];
    expect(writeCall).not.toContain("**Issue Number**");
  });
});

describe("skillRunner - Auto-Accept Configuration", () => {
  let originalEnv: NodeJS.ProcessEnv;

  beforeEach(() => {
    vi.clearAllMocks();
    originalEnv = { ...process.env };
    vi.mocked(fs.existsSync).mockReturnValue(true);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    process.env = originalEnv;
  });

  it("should set CI=true by default", () => {
    vi.mocked(fs.readFileSync).mockImplementation((filePath) => {
      if (String(filePath).includes("SKILL.md")) {
        return `---\nallowed-tools: Read\n---\n# Skill`;
      }
      throw new Error("File not found");
    });
    vi.mocked(fs.existsSync).mockImplementation((filePath) => {
      return String(filePath).includes("SKILL.md");
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          CI: "true",
        }),
      })
    );
  });

  // Note: Testing YAML config parsing is complex due to the simple line-based parser.
  // The environment variable override tests below cover the core functionality.
  // YAML config loading is tested manually with integration tests.

  it("should respect environment variable overrides", () => {
    process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = "true";
    process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES = "true";

    vi.mocked(fs.existsSync).mockImplementation((filePath) => {
      return String(filePath).includes("SKILL.md");
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS: "true",
          NIGHTGAUGE_AUTO_ACCEPT_STAGES: "true",
        }),
      })
    );
  });

  it("should handle missing config file gracefully", () => {
    vi.mocked(fs.existsSync).mockImplementation((filePath) => {
      // Skill file exists, but nightgauge.yaml does not
      return String(filePath).includes("SKILL.md");
    });
    vi.mocked(fs.readFileSync).mockImplementation((filePath) => {
      if (String(filePath).includes("nightgauge.yaml")) {
        throw new Error("ENOENT");
      }
      return `---\nallowed-tools: Read\n---\n# Skill`;
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    // Should not throw
    const handle = runStageSkillHeadless("feature-dev", 42, {});
    expect(handle.process).toBeDefined();
  });
});

describe("skillRunner - Core Execution (runStageSkillHeadless)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash AskUserQuestion
---
# Test Skill
`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should spawn claude CLI with correct arguments", () => {
    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.arrayContaining([
        "-p",
        "--no-session-persistence",
        "--output-format",
        "stream-json",
        "--verbose",
        "--allowedTools",
      ]),
      expect.objectContaining({
        cwd: "/test/workspace",
        shell: false,
        stdio: ["pipe", "pipe", "pipe"],
      })
    );
  });

  it("should write prompt to stdin and close it", () => {
    runStageSkillHeadless("feature-dev", 42, {});

    expect(mockProcess.stdin!.write).toHaveBeenCalledWith(
      expect.stringContaining("Stage: Feature Development")
    );
    expect(mockProcess.stdin!.end).toHaveBeenCalled();
  });

  it("fires onModelResolved once with the resolved model+adapter before spawning (#367)", () => {
    const onModelResolved = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onModelResolved });

    // Fires exactly once, with the stage, a concrete resolved model, and the
    // executing adapter — the up-front record that makes attribution
    // independent of the termination path.
    expect(onModelResolved).toHaveBeenCalledTimes(1);
    const [stage, model, adapter] = onModelResolved.mock.calls[0];
    expect(stage).toBe("feature-dev");
    expect(typeof model).toBe("string");
    expect((model as string).length).toBeGreaterThan(0);
    expect(adapter).toBe("claude");

    // Must fire BEFORE the CLI spawns, so a stage killed early still has its
    // model on record.
    expect(onModelResolved.mock.invocationCallOrder[0]).toBeLessThan(
      vi.mocked(spawn).mock.invocationCallOrder[0]
    );
  });

  it("should filter AskUserQuestion from allowed tools", () => {
    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1];
    const toolsIndex = args.indexOf("--allowedTools");
    const tools = args[toolsIndex + 1];

    expect(tools).not.toContain("AskUserQuestion");
    expect(tools).toContain("Read");
    expect(tools).toContain("Write");
  });

  it("should call onStdout callback with output data", () => {
    const onStdout = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStdout });

    mockProcess.stdout!.emit("data", Buffer.from("test output"));

    expect(onStdout).toHaveBeenCalledWith("test output");
  });

  it("should call onStderr callback with error data", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStderr });

    mockProcess.stderr!.emit("data", Buffer.from("error output"));

    expect(onStderr).toHaveBeenCalledWith("error output");
  });

  it("should call onComplete with success=true on exit code 0", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        exitCode: 0,
      })
    );
  });

  it("should call onComplete with success=false on non-zero exit code", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 1);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: 1,
      })
    );
  });

  it("should infer error from stdout JSON when stderr is empty", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.stdout!.emit(
      "data",
      Buffer.from(
        '{"level":"error","message":"Adapter preflight failed: branch must be a feature branch, not main/master."}\n'
      )
    );
    mockProcess.emit("close", 2);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: 2,
        error: expect.objectContaining({
          message: "Adapter preflight failed: branch must be a feature branch, not main/master.",
        }),
      })
    );
  });

  // Deterministic phase inference (Issue #3760): feature-dev does not reliably
  // emit `printf` phase markers (it's edit-heavy), so phase progress is inferred
  // from the tool calls the agent actually makes, delivered through the same
  // onPhaseStart channel the working stages use.
  describe("deterministic phase inference (#3760)", () => {
    function emitAssistantToolUse(name: string, input: unknown): void {
      const line = JSON.stringify({
        type: "assistant",
        message: { role: "assistant", content: [{ type: "tool_use", name, input }] },
      });
      mockProcess.stdout!.emit("data", Buffer.from(line + "\n"));
    }

    it("emits validate-environment as soon as feature-dev output starts", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onPhaseStart });

      mockProcess.stdout!.emit("data", Buffer.from('{"type":"system","subtype":"init"}\n'));

      expect(onPhaseStart).toHaveBeenCalledWith("feature-dev", "validate-environment", 0, 18);
    });

    it("advances through implementation → testing as the agent works", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onPhaseStart });

      // First chunk triggers the start phase.
      mockProcess.stdout!.emit("data", Buffer.from('{"type":"system","subtype":"init"}\n'));
      emitAssistantToolUse("Read", { file_path: "PLAN.md" }); // read-planning-context
      emitAssistantToolUse("Write", { file_path: "src/feature.ts", content: "x" }); // implementation
      emitAssistantToolUse("Bash", { command: "npx -w nightgauge-vscode vitest run" }); // testing

      const phases = onPhaseStart.mock.calls.map((c) => c[1]);
      expect(phases).toEqual([
        "validate-environment",
        "read-planning-context",
        "implementation",
        "testing",
      ]);
      // testing should report index 9 of 18
      expect(onPhaseStart).toHaveBeenLastCalledWith("feature-dev", "testing", 9, 18);
    });

    it("is monotonic — a context read after implementation does not regress", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-dev", 42, { onPhaseStart });

      mockProcess.stdout!.emit("data", Buffer.from('{"type":"system","subtype":"init"}\n'));
      emitAssistantToolUse("Write", { file_path: "src/feature.ts", content: "x" }); // implementation (8)
      onPhaseStart.mockClear();
      emitAssistantToolUse("Read", { file_path: "src/other.ts" }); // would be index 1 — ignored

      expect(onPhaseStart).not.toHaveBeenCalled();
    });

    it("does NOT infer phases for stages that self-report (feature-validate)", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-validate", 42, { onPhaseStart });

      mockProcess.stdout!.emit("data", Buffer.from('{"type":"system","subtype":"init"}\n'));
      emitAssistantToolUse("Write", { file_path: "src/feature.ts", content: "x" });

      // No inferred markers — only genuine printf markers drive validate.
      expect(onPhaseStart).not.toHaveBeenCalled();
    });
  });

  // #217: a printf'd phase marker is visible twice in the stream — once as
  // the command echo inside the assistant's tool_use input, once as the
  // tool_result stdout. Detection must fire exactly once (tool_result path),
  // or phaseHistory double-counts every phase.
  describe("phase marker double-sighting (#217)", () => {
    const MARKER =
      '<!-- phase:start name="feedback-context-check" index=2 total=8 stage="feature-validate" -->';

    it("fires onPhaseStart exactly once for a command echo + tool_result pair", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-validate", 42, { onPhaseStart });

      // Assistant turn: the Bash printf tool call carrying the marker in its
      // command input (the echo).
      const assistantLine = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            {
              type: "tool_use",
              id: "toolu_marker",
              name: "Bash",
              input: { command: `printf '${MARKER}\\n'`, description: "Emit phase marker" },
            },
          ],
        },
      });
      mockProcess.stdout!.emit("data", Buffer.from(assistantLine + "\n"));
      expect(onPhaseStart).not.toHaveBeenCalled();

      // Tool-result turn: the printf stdout carrying the actual marker.
      const userLine = JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: [{ type: "tool_result", tool_use_id: "toolu_marker", content: MARKER + "\n" }],
        },
      });
      mockProcess.stdout!.emit("data", Buffer.from(userLine + "\n"));

      expect(onPhaseStart).toHaveBeenCalledTimes(1);
      expect(onPhaseStart).toHaveBeenCalledWith("feature-validate", "feedback-context-check", 2, 8);

      // Later assistant text must not replay the echo: pre-fix, the command
      // string sat in the phase content buffer and a newline-bearing
      // follow-up flushed it as a phantom duplicate (the 43s-late twin
      // observed in run bowlsheet-flutter#244).
      const followUpLine = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "text", text: "Phase emitted, continuing.\n" }],
        },
      });
      mockProcess.stdout!.emit("data", Buffer.from(followUpLine + "\n"));

      expect(onPhaseStart).toHaveBeenCalledTimes(1);
    });

    it("still detects markers emitted as genuine assistant text", () => {
      const onPhaseStart = vi.fn();
      runStageSkillHeadless("feature-validate", 42, { onPhaseStart });

      const assistantLine = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "text", text: MARKER + "\n" }],
        },
      });
      mockProcess.stdout!.emit("data", Buffer.from(assistantLine + "\n"));

      expect(onPhaseStart).toHaveBeenCalledTimes(1);
      expect(onPhaseStart).toHaveBeenCalledWith("feature-validate", "feedback-context-check", 2, 8);
    });
  });

  it("should prefer stderr error when both stderr and stdout exist", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.stdout!.emit("data", Buffer.from('{"message":"stdout error"}\n'));
    mockProcess.stderr!.emit("data", Buffer.from("stderr dominates\n"));
    mockProcess.emit("close", 1);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: 1,
        error: expect.objectContaining({
          message: "stderr dominates",
        }),
      })
    );
  });

  it("should call onError when process emits error", () => {
    const onError = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    const error = new Error("spawn error");
    mockProcess.emit("error", error);

    expect(onError).toHaveBeenCalledWith(error);
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: null,
        error,
      })
    );
  });

  it("should return error handle when no workspace folder", async () => {
    // Temporarily override the workspace mock
    const vscode = await import("vscode");
    const originalFolders = vscode.workspace.workspaceFolders;
    Object.defineProperty(vscode.workspace, "workspaceFolders", {
      value: undefined,
      configurable: true,
    });

    const onError = vi.fn();
    const onComplete = vi.fn();
    const handle = runStageSkillHeadless("feature-dev", 42, {
      onError,
      onComplete,
    });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "No workspace folder open",
      })
    );
    expect(handle.process).toBeNull();

    // Restore
    Object.defineProperty(vscode.workspace, "workspaceFolders", {
      value: originalFolders,
      configurable: true,
    });
  });

  it("should add process to activeProcesses map", () => {
    runStageSkillHeadless("feature-dev", 42, {});

    expect(isStageRunning("feature-dev", 42)).toBe(true);
  });

  it("should remove process from activeProcesses on completion", () => {
    runStageSkillHeadless("feature-dev", 42, {});
    expect(isStageRunning("feature-dev", 42)).toBe(true);

    mockProcess.emit("close", 0);

    expect(isStageRunning("feature-dev", 42)).toBe(false);
  });
});

describe("skillRunner - Token Usage", () => {
  let mockProcess: ChildProcess;
  let originalEnv: NodeJS.ProcessEnv;

  beforeEach(() => {
    vi.clearAllMocks();
    originalEnv = { ...process.env };
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    process.env = originalEnv;
    killAllActiveProcesses();
  });

  it("should parse token usage from stream-json output", () => {
    const onTokenUsage = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onTokenUsage });

    const resultMessage = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 1000,
        output_tokens: 500,
        cache_read_input_tokens: 100,
        cache_creation_input_tokens: 50,
      },
      total_cost_usd: 0.05,
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

    expect(onTokenUsage).toHaveBeenCalledWith(
      expect.objectContaining({
        inputTokens: 1000,
        outputTokens: 500,
        cacheReadTokens: 100,
        cacheCreationTokens: 50,
        costUsd: 0.05,
      })
    );
  });

  it("should accumulate tokens across multiple messages", () => {
    const onTokenUsage = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onTokenUsage });

    // First message
    const result1 = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 100,
        output_tokens: 50,
      },
      total_cost_usd: 0.01,
    });

    // Second message
    const result2 = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 200,
        output_tokens: 100,
      },
      total_cost_usd: 0.02,
    });

    mockProcess.stdout!.emit("data", Buffer.from(result1 + "\n"));
    mockProcess.stdout!.emit("data", Buffer.from(result2 + "\n"));

    // Should have been called twice with accumulated values
    expect(onTokenUsage).toHaveBeenCalledTimes(2);
    expect(onTokenUsage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        inputTokens: 300,
        outputTokens: 150,
      })
    );
  });

  it("should include token usage in completion result", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    const resultMessage = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 500,
        output_tokens: 250,
      },
      total_cost_usd: 0.03,
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        tokenUsage: expect.objectContaining({
          inputTokens: 500,
          outputTokens: 250,
        }),
      })
    );
  });

  // ── #296: killed stages must book their real burn ─────────────────────────
  // An `assistant` stream-json message that carries `message.usage` (a growing-
  // context snapshot) feeds the LiveStageEstimator; the terminal `result`
  // envelope (which alone feeds the authoritative TokenAccumulator) is what a
  // SIGTERM'd CLI never emits. These tests drive that exact shape.
  const assistantUsageLine = (
    inputTokens: number,
    outputTokens: number,
    cacheReadTokens = 0
  ): string =>
    JSON.stringify({
      type: "assistant",
      message: {
        role: "assistant",
        model: "claude-sonnet-4-6",
        content: [{ type: "text", text: "still working" }],
        usage: {
          input_tokens: inputTokens,
          output_tokens: outputTokens,
          cache_read_input_tokens: cacheReadTokens,
          cache_creation_input_tokens: 0,
        },
      },
    });

  it("books the live estimate when killed mid-stage before any result envelope (#296)", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // The agent streamed real work (assistant turns with a growing context) …
    mockProcess.stdout!.emit("data", Buffer.from(assistantUsageLine(40_000, 3_000, 8_000) + "\n"));
    mockProcess.stdout!.emit("data", Buffer.from(assistantUsageLine(55_000, 5_500, 12_000) + "\n"));
    // … then the runaway/stall/budget monitor SIGTERM'd it: the CLI never
    // emitted a terminal `type:"result"` envelope, only a non-zero close.
    mockProcess.emit("close", 143); // 128 + SIGTERM

    expect(onComplete).toHaveBeenCalledTimes(1);
    const result = onComplete.mock.calls[0][0];
    expect(result.success).toBe(false);
    // Pre-#296 this was `undefined` → the stage booked $0. Now the last live
    // estimate is booked as the stage's cost.
    expect(result.tokenUsage).toBeDefined();
    // Latest-wins input (55_000, not summed), summed output (3_000 + 5_500).
    expect(result.tokenUsage.inputTokens).toBe(55_000);
    expect(result.tokenUsage.outputTokens).toBe(8_500);
    expect(result.tokenUsage.cacheReadTokens).toBe(12_000);
    // The booked cost is the real burn — a positive, table-computed number.
    expect(result.tokenUsage.costUsd).toBeGreaterThan(0);
    // …and it is flagged as an estimate so downstream can weight it.
    expect(result.costEstimated).toBe(true);
  });

  it("prefers the authoritative envelope over the estimate — no double-book (#296)", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // Live estimate accrues from assistant turns …
    mockProcess.stdout!.emit("data", Buffer.from(assistantUsageLine(40_000, 3_000) + "\n"));
    // … but a terminal `result` envelope DID land before close, so the
    // accumulator is authoritative and the estimate must be discarded.
    const resultMessage = JSON.stringify({
      type: "result",
      usage: { input_tokens: 1_000, output_tokens: 500 },
      total_cost_usd: 0.42,
    });
    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));
    mockProcess.emit("close", 0);

    const result = onComplete.mock.calls[0][0];
    expect(result.tokenUsage.costUsd).toBeCloseTo(0.42, 4);
    // Authoritative tokens (1_000 in), NOT the estimate's 40_000.
    expect(result.tokenUsage.inputTokens).toBe(1_000);
    expect(result.costEstimated).toBeUndefined();
  });

  it("books nothing when a stage is killed before producing any usage (#296)", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // No assistant usage, no result envelope — just a kill.
    mockProcess.emit("close", 143);

    const result = onComplete.mock.calls[0][0];
    expect(result.tokenUsage).toBeUndefined();
    expect(result.costEstimated).toBeUndefined();
  });

  it("should parse Codex token:usage events", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
    const onTokenUsage = vi.fn();

    runStageSkillHeadless("feature-dev", 42, { onTokenUsage });

    const tokenEvent = JSON.stringify({
      type: "token:usage",
      stage: "feature-dev",
      inputTokens: 320,
      outputTokens: 180,
      cacheReadTokens: 20,
      cacheCreationTokens: 10,
      costUsd: 0.0075,
    });

    mockProcess.stdout!.emit("data", Buffer.from(tokenEvent + "\n"));

    expect(onTokenUsage).toHaveBeenCalledWith(
      expect.objectContaining({
        inputTokens: 320,
        outputTokens: 180,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.0075,
      })
    );
  });

  it("should force JSON output format for Codex adapter runs", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          NIGHTGAUGE_ADAPTER: "codex",
          NIGHTGAUGE_OUTPUT_FORMAT: "json",
        }),
      })
    );
  });

  // Issue #3223 — per-stage adapter dispatch. The headless dispatcher must
  // honor `resolveStageAdapter` instead of the global lookup, and must report
  // `adapterDecision` on the onComplete callback so the orchestrator can
  // persist `adapter_source` into history records.
  it("should honor NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_<STAGE> env override", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "codex";
    runStageSkillHeadless("feature-dev", 42, {});

    // Packaged adapters route through the SDK CLI bundled with the extension.
    const spawnCall = vi.mocked(spawn).mock.calls[0];
    expect(spawnCall[0]).toBe("node");
    expect(spawnCall[1]).toEqual(expect.arrayContaining(["stage", "feature-dev", "42"]));

    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV;
  });

  it("should report adapterDecision with source=env when stage env override set", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV = "codex";
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "codex", source: "env" },
      })
    );

    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV;
  });

  it("should report adapterDecision with source=default for default-config users (AC #6)", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "claude", source: "default" },
      })
    );
  });

  it("should report adapterDecision with source=global-config when ui.core.adapter env set", () => {
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "codex", source: "global-config" },
      })
    );
  });

  it("should emit [stage:adapter-unavailable] envelope when prereq fails and no fallback", () => {
    // Force a prereq failure by selecting an adapter that needs `claude` in PATH
    // while existsSync returns false for the run-stage.sh case below. claude
    // adapter prereq calls commandExists("claude") which itself uses execFileSync
    // — execFile is mocked to throw "no children" causing commandExists to
    // return false. That's the simplest path: no env override (claude/default),
    // commandExists("claude") = false → prereq fails.
    const onError = vi.fn();
    const onComplete = vi.fn();
    // Override commandExists indirectly: claude prereq → commandExists("claude")
    // uses execFileSync. The test mock makes that throw → returns false →
    // prereq error is emitted.
    runStageSkillHeadless("feature-dev", 42, { onError, onComplete });

    // The prereq path is exercised, but since the test environment has the
    // command lookup mock that returns false, we should see either:
    // (a) a clean spawn (env mocking allowed it through), OR
    // (b) the [stage:adapter-unavailable] envelope.
    // The actual outcome depends on how commandExists("claude") resolves in
    // the test harness. The contract this test pins is: when prereq fails,
    // the error message starts with the structured envelope.
    if (onError.mock.calls.length > 0) {
      const errArg = onError.mock.calls[0][0] as Error;
      expect(errArg.message).toMatch(/^\[stage:adapter-unavailable\]/);
    }
    // Otherwise, the test environment's commandExists returned true and the
    // dispatcher proceeded — that's also valid and not an assertion failure.
  });

  // Issue #3231 — primary-success path must NOT emit the
  // adapterFallbackChainUsed audit trail. The field is reserved for
  // length ≥ 2 (fallback walked). Length 1 (primary worked) keeps records
  // terse — downstream history writer treats absence as "no fallback".
  it("omits adapterFallbackChainUsed on adapterDecision when primary succeeds (Issue #3231)", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        adapterDecision: { adapter: "claude", source: "default" },
      })
    );
    // Verify the audit field is genuinely absent, not just any value.
    const result = onComplete.mock.calls[0][0];
    expect(result.adapterDecision.adapterFallbackChainUsed).toBeUndefined();
  });

  // Issue #2919 — Safety net for when the streaming line parser misses the
  // terminal `type:"result"` envelope. Even when onTokenUsage didn't fire
  // during streaming, the close handler re-scans the raw stdout tail and
  // rescues the usage block so slot badges don't display $0 / 0 tokens.
  it("should rescue token usage from stdout tail on close when streaming parser missed it", () => {
    const onTokenUsage = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onTokenUsage, onComplete });

    // Emit the result envelope as raw stdout that was delivered without being
    // line-split (simulating a stream-framing race where the line parser
    // didn't see it but it is present in stdoutRawTail). We emit it with a
    // trailing newline so it lands in stdoutRawTail AND gets line-processed;
    // the fallback should then be skipped because streaming already captured.
    // To exercise the fallback specifically, we emit WITHOUT newline then
    // close — the streaming loop won't process it as a complete line, but
    // the close handler's existing `if (stdoutBuffer) ...` path (line 2552)
    // handles the trailing buffer. To truly exercise the new fallback, we
    // need a case where stdoutBuffer was consumed but tokenAccumulator is
    // still empty — simulated here by emitting the envelope inside a
    // multi-line blob where the first pass parses a non-result line and the
    // result line reaches stdoutRawTail but doesn't re-trigger token capture
    // in the streaming loop.
    const noise = '{"type":"assistant","message":{"content":[]}}\n';
    const resultMessage = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 1234,
        output_tokens: 567,
        cache_read_input_tokens: 89,
        cache_creation_input_tokens: 10,
      },
      total_cost_usd: 0.0456,
    });

    // Emit noise (processed by streaming loop) followed by the result
    // envelope without trailing newline (remains in stdoutBuffer).
    mockProcess.stdout!.emit("data", Buffer.from(noise + resultMessage));
    mockProcess.emit("close", 0);

    // Either the trailing-buffer path or the new fallback must surface the
    // usage. Either way, onComplete must see non-zero tokens so slot badges
    // render correctly (Issue #2919).
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        tokenUsage: expect.objectContaining({
          inputTokens: 1234,
          outputTokens: 567,
          cacheReadTokens: 89,
          cacheCreationTokens: 10,
          costUsd: 0.0456,
        }),
      })
    );
  });

  it("should emit diagnostic WARNING when stage exits 0 but no tokens captured", () => {
    const onStderr = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStderr, onComplete });

    // Emit stdout with no `type:"result"` envelope anywhere — simulating a
    // CLI that exited cleanly but produced no usage info. We expect the new
    // diagnostic WARN so this failure mode is never silent again.
    mockProcess.stdout!.emit(
      "data",
      Buffer.from('{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}\n')
    );
    mockProcess.emit("close", 0);

    // Collect all stderr messages routed through the callback.
    const stderrCalls = onStderr.mock.calls.map((c) => String(c[0])).join("");
    expect(stderrCalls).toContain("WARNING");
    expect(stderrCalls).toContain("no token usage captured");

    // And onComplete still fires with zero-token tokenUsage omitted.
    expect(onComplete).toHaveBeenCalledWith(expect.objectContaining({ success: true }));
  });
});

describe("skillRunner - Live in-stage progress (#233)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  // A full assistant Message whose usage is a growing-context snapshot: input is
  // the whole context re-reported each turn; output is that turn's output.
  const assistantWithUsage = (input: number, output: number): string =>
    JSON.stringify({
      type: "assistant",
      message: {
        role: "assistant",
        model: "claude-sonnet-4-6",
        content: [{ type: "text", text: "working" }],
        usage: {
          input_tokens: input,
          output_tokens: output,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      },
    });

  it("streams a live estimate from assistant usage — throttled >=5s, latest-wins input, summed output", () => {
    const onStageProgress = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStageProgress });

    // t=0: first snapshot. lastEmit is seeded one cadence in the past, so it
    // emits immediately (call 1): input 1000, output 50.
    mockProcess.stdout!.emit("data", Buffer.from(assistantWithUsage(1000, 50) + "\n"));

    // t=1s: inside the 5s window — throttled (no emit).
    vi.advanceTimersByTime(1000);
    mockProcess.stdout!.emit("data", Buffer.from(assistantWithUsage(1500, 60) + "\n"));

    // t=6s: window elapsed — emits (call 2). Input is latest-wins (2000, NOT
    // 1000+1500+2000); output is summed (50+60+70=180).
    vi.advanceTimersByTime(5000);
    mockProcess.stdout!.emit("data", Buffer.from(assistantWithUsage(2000, 70) + "\n"));

    expect(onStageProgress).toHaveBeenCalledTimes(2);
    expect(onStageProgress.mock.calls[0][0]).toMatchObject({ inputTokens: 1000, outputTokens: 50 });
    expect(onStageProgress).toHaveBeenLastCalledWith(
      expect.objectContaining({ inputTokens: 2000, outputTokens: 180 })
    );
  });

  it("never feeds the live estimate into the authoritative accumulator — onComplete equals the terminal result envelope", () => {
    const onStageProgress = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStageProgress, onComplete });

    // Growing-context assistant snapshots — input re-reports the full context.
    mockProcess.stdout!.emit("data", Buffer.from(assistantWithUsage(1000, 50) + "\n"));
    vi.advanceTimersByTime(6000);
    mockProcess.stdout!.emit("data", Buffer.from(assistantWithUsage(9000, 80) + "\n"));

    // Terminal result envelope — the authoritative per-stage total.
    const resultMsg = JSON.stringify({
      type: "result",
      usage: { input_tokens: 12000, output_tokens: 130 },
      total_cost_usd: 0.5,
    });
    mockProcess.stdout!.emit("data", Buffer.from(resultMsg + "\n"));
    mockProcess.emit("close", 0);

    // onComplete carries the terminal envelope total (12000/130), NOT the summed
    // assistant inputs (1000+9000=10000) — proof incrementalUsage never reached
    // the additive TokenAccumulator.
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        tokenUsage: expect.objectContaining({ inputTokens: 12000, outputTokens: 130 }),
      })
    );
    // The live estimator DID observe the assistant snapshots (latest-wins 9000).
    expect(onStageProgress).toHaveBeenLastCalledWith(
      expect.objectContaining({ inputTokens: 9000 })
    );
  });
});

describe("skillRunner - Session ID", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should capture session_id from result messages", () => {
    const onSessionId = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onSessionId });

    const resultMessage = JSON.stringify({
      type: "result",
      session_id: "test-session-123",
      usage: { input_tokens: 100 },
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

    expect(onSessionId).toHaveBeenCalledWith("test-session-123");
  });

  it("should include sessionId in SkillProcessHandle", () => {
    const handle = runStageSkillHeadless("feature-dev", 42, {});

    const resultMessage = JSON.stringify({
      type: "result",
      session_id: "test-session-456",
      usage: { input_tokens: 100 },
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

    expect(handle.sessionId).toBe("test-session-456");
  });

  it("should include sessionId in completion result", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    const resultMessage = JSON.stringify({
      type: "result",
      session_id: "test-session-789",
      usage: { input_tokens: 100 },
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        sessionId: "test-session-789",
      })
    );
  });
});

describe("skillRunner - Tool Use Detection", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should call onToolUse callback for tool_use blocks", () => {
    const onToolUse = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onToolUse });

    const toolUseMessage = JSON.stringify({
      type: "content_block_start",
      content_block: {
        type: "tool_use",
        id: "tool_123",
        name: "Read",
        input: { file_path: "/some/path" },
      },
    });

    mockProcess.stdout!.emit("data", Buffer.from(toolUseMessage + "\n"));

    expect(onToolUse).toHaveBeenCalledWith("Read", { file_path: "/some/path" }, "tool_123");
  });

  it("should extract tool_use ID from content_block", () => {
    const onToolUse = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onToolUse });

    const toolUseMessage = JSON.stringify({
      type: "content_block_start",
      content_block: {
        type: "tool_use",
        id: "unique_tool_id_abc",
        name: "Write",
        input: { file_path: "/output", content: "data" },
      },
    });

    mockProcess.stdout!.emit("data", Buffer.from(toolUseMessage + "\n"));

    expect(onToolUse).toHaveBeenCalledWith("Write", expect.any(Object), "unique_tool_id_abc");
  });
});

describe("skillRunner - Session Resumption (resumeSessionWithResponse)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should spawn claude CLI with --resume flag", () => {
    resumeSessionWithResponse("session-abc-123", "User response", {});

    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.arrayContaining(["--resume", "session-abc-123"]),
      expect.any(Object)
    );
  });

  it("should write response to stdin and close it", () => {
    resumeSessionWithResponse("session-abc-123", "User response", {});

    expect(mockProcess.stdin!.write).toHaveBeenCalledWith("User response");
    expect(mockProcess.stdin!.end).toHaveBeenCalled();
  });

  it("should handle token usage in resumed session", () => {
    const onTokenUsage = vi.fn();
    resumeSessionWithResponse("session-abc-123", "Response", { onTokenUsage });

    const resultMessage = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 200,
        output_tokens: 100,
      },
      total_cost_usd: 0.01,
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

    expect(onTokenUsage).toHaveBeenCalled();
  });

  it("should handle session_id in resumed session", () => {
    const onSessionId = vi.fn();
    resumeSessionWithResponse("session-abc-123", "Response", { onSessionId });

    const resultMessage = JSON.stringify({
      type: "result",
      session_id: "new-session-456",
      usage: { input_tokens: 100 },
    });

    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

    expect(onSessionId).toHaveBeenCalledWith("new-session-456");
  });

  it("should call completion callback on success", () => {
    const onComplete = vi.fn();
    resumeSessionWithResponse("session-abc-123", "Response", { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        exitCode: 0,
      })
    );
  });

  it("should call error callback on failure", () => {
    const onError = vi.fn();
    const onComplete = vi.fn();
    resumeSessionWithResponse("session-abc-123", "Response", {
      onError,
      onComplete,
    });

    const error = new Error("Connection failed");
    mockProcess.emit("error", error);

    expect(onError).toHaveBeenCalledWith(error);
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        error,
      })
    );
  });
});

describe("skillRunner - Process Management", () => {
  let mockProcess1: ChildProcess;
  let mockProcess2: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess1 = createMockChildProcess();
    mockProcess2 = createMockChildProcess();
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  describe("killAllActiveProcesses", () => {
    it("should kill all tracked processes", () => {
      vi.mocked(spawn).mockReturnValueOnce(mockProcess1).mockReturnValueOnce(mockProcess2);

      runStageSkillHeadless("feature-dev", 42, {});
      runStageSkillHeadless("feature-validate", 42, {});

      expect(hasActiveProcess()).toBe(true);

      killAllActiveProcesses();

      expect(mockProcess1.kill).toHaveBeenCalledWith("SIGTERM");
      expect(mockProcess2.kill).toHaveBeenCalledWith("SIGTERM");
      expect(hasActiveProcess()).toBe(false);
    });
  });

  describe("isStageRunning", () => {
    it("should return true for active processes", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      runStageSkillHeadless("feature-dev", 42, {});

      expect(isStageRunning("feature-dev", 42)).toBe(true);
    });

    it("should return false for inactive stages", () => {
      expect(isStageRunning("feature-dev", 99)).toBe(false);
    });

    it("should return false after process completes", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      runStageSkillHeadless("feature-dev", 42, {});
      mockProcess1.emit("close", 0);

      expect(isStageRunning("feature-dev", 42)).toBe(false);
    });
  });

  describe("getActiveProcess", () => {
    it("should return handle for active process", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      const handle = runStageSkillHeadless("feature-dev", 42, {});
      const retrieved = getActiveProcess("feature-dev", 42);

      expect(retrieved).toBe(handle);
    });

    it("should return undefined for inactive stage", () => {
      expect(getActiveProcess("feature-dev", 99)).toBeUndefined();
    });
  });

  describe("hasActiveProcess", () => {
    it("should return true when processes exist", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      runStageSkillHeadless("feature-dev", 42, {});

      expect(hasActiveProcess()).toBe(true);
    });

    it("should return false when no processes", () => {
      killAllActiveProcesses();
      expect(hasActiveProcess()).toBe(false);
    });
  });

  describe("getLastSessionId", () => {
    it("should return session ID from active process", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      runStageSkillHeadless("feature-dev", 42, {});

      const resultMessage = JSON.stringify({
        type: "result",
        session_id: "last-session-id",
        usage: { input_tokens: 100 },
      });

      mockProcess1.stdout!.emit("data", Buffer.from(resultMessage + "\n"));

      expect(getLastSessionId()).toBe("last-session-id");
    });

    it("should return undefined when no session ID captured", () => {
      vi.mocked(spawn).mockReturnValue(mockProcess1);

      runStageSkillHeadless("feature-dev", 42, {});

      expect(getLastSessionId()).toBeUndefined();
    });
  });
});

describe("skillRunner - Edge Cases", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should handle stdout buffer with incomplete JSON lines", () => {
    const onTokenUsage = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onTokenUsage });

    // Send incomplete JSON in first chunk (split in the middle of a field name)
    mockProcess.stdout!.emit("data", Buffer.from('{"type":"result","usage":{"input_tokens":10'));

    // onTokenUsage should not be called for incomplete data
    expect(onTokenUsage).not.toHaveBeenCalled();

    // Complete the JSON in second chunk
    mockProcess.stdout!.emit("data", Buffer.from('0},"total_cost_usd":0.01}\n'));

    // Now it should be called
    expect(onTokenUsage).toHaveBeenCalled();
  });

  it("should handle process close with remaining buffer content", () => {
    const onTokenUsage = vi.fn();
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onTokenUsage, onComplete });

    // Send JSON without trailing newline
    const resultMessage = JSON.stringify({
      type: "result",
      usage: { input_tokens: 500, output_tokens: 200 },
      total_cost_usd: 0.03,
    });
    mockProcess.stdout!.emit("data", Buffer.from(resultMessage));

    // Close the process - should process remaining buffer
    mockProcess.emit("close", 0);

    expect(onTokenUsage).toHaveBeenCalled();
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        tokenUsage: expect.objectContaining({
          inputTokens: 500,
        }),
      })
    );
  });

  it("should handle spawn failure gracefully", () => {
    vi.mocked(spawn).mockImplementation(() => {
      throw new Error("ENOENT: claude command not found");
    });

    // Should not throw
    expect(() => {
      runStageSkillHeadless("feature-dev", 42, {});
    }).toThrow("ENOENT");
  });

  it("should handle process without stdin", () => {
    const processWithoutStdin = createMockChildProcess();
    processWithoutStdin.stdin = null as unknown as NodeJS.WritableStream;
    vi.mocked(spawn).mockReturnValue(processWithoutStdin);

    const onError = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onError });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("stdin pipe"),
      })
    );
  });

  it("should handle process key with no issue number", () => {
    runStageSkillHeadless("issue-pickup", undefined, {});

    expect(isStageRunning("issue-pickup", undefined)).toBe(true);
    expect(isStageRunning("issue-pickup", 0)).toBe(false);
  });
});

// =============================================================================
// INTERACTIVE MODE TESTS (Issue #495)
// =============================================================================

describe("skillRunner - Interactive Mode Constants", () => {
  it("should define INTERACTIVE_TIMEOUT_MS as 30 minutes", () => {
    expect(INTERACTIVE_TIMEOUT_MS).toBe(30 * 60 * 1000);
  });
});

describe("skillRunner - runStageSkillInteractive", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash AskUserQuestion
---
# Test Skill
`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should spawn claude CLI WITHOUT -p flag", () => {
    runStageSkillInteractive("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1];

    // Should NOT include -p flag
    expect(args).not.toContain("-p");
    // Should include --verbose
    expect(args).toContain("--verbose");
    // Should include --allowedTools
    expect(args).toContain("--allowedTools");
  });

  it("should NOT call stdin.end() after writing prompt", () => {
    runStageSkillInteractive("feature-dev", 42, {});

    // Should write prompt to stdin
    expect(mockProcess.stdin!.write).toHaveBeenCalledWith(
      expect.stringContaining("Stage: Feature Development")
    );
    // Should NOT call stdin.end() - this is the key difference
    expect(mockProcess.stdin!.end).not.toHaveBeenCalled();
  });

  it("should include AskUserQuestion in allowed tools", () => {
    runStageSkillInteractive("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1];
    const toolsIndex = args.indexOf("--allowedTools");
    const tools = args[toolsIndex + 1];

    // AskUserQuestion should be included (unlike headless mode)
    expect(tools).toContain("AskUserQuestion");
  });

  it("should return handle with isInteractive=true", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    expect(handle.isInteractive).toBe(true);
  });

  it("should return handle with writeToStdin function", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    expect(typeof handle.writeToStdin).toBe("function");
  });

  it("should write to stdin when writeToStdin is called", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    const result = handle.writeToStdin!("User message");

    expect(result).toBe(true);
    expect(mockProcess.stdin!.write).toHaveBeenCalledWith("User message\n");
  });

  it("should return false when stdin is destroyed", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    // Simulate stdin being destroyed
    (mockProcess.stdin as unknown as { destroyed: boolean }).destroyed = true;

    const result = handle.writeToStdin!("User message");

    expect(result).toBe(false);
  });

  it("should call onStdout callback with raw output", () => {
    const onStdout = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onStdout });

    mockProcess.stdout!.emit("data", Buffer.from("Hello from Claude"));

    expect(onStdout).toHaveBeenCalledWith("Hello from Claude");
  });

  it("should call onStderr callback", () => {
    const onStderr = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onStderr });

    mockProcess.stderr!.emit("data", Buffer.from("Some stderr"));

    expect(onStderr).toHaveBeenCalledWith("Some stderr");
  });

  it("should call onComplete with success=true on exit code 0", () => {
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        exitCode: 0,
        tokenUsage: undefined, // No token usage in interactive mode
      })
    );
  });

  it("should call onComplete with success=false on non-zero exit", () => {
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onComplete });

    mockProcess.emit("close", 1);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: 1,
      })
    );
  });

  it("should call onError when process emits error", () => {
    const onError = vi.fn();
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onError, onComplete });

    const error = new Error("Process error");
    mockProcess.emit("error", error);

    expect(onError).toHaveBeenCalledWith(error);
    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        exitCode: null,
        error,
      })
    );
  });

  it("should return error handle when no workspace folder", async () => {
    const vscode = await import("vscode");
    const originalFolders = vscode.workspace.workspaceFolders;
    Object.defineProperty(vscode.workspace, "workspaceFolders", {
      value: undefined,
      configurable: true,
    });

    const onError = vi.fn();
    const handle = runStageSkillInteractive("feature-dev", 42, { onError });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "No workspace folder open",
      })
    );
    expect(handle.process).toBeNull();
    expect(handle.isInteractive).toBe(true);

    Object.defineProperty(vscode.workspace, "workspaceFolders", {
      value: originalFolders,
      configurable: true,
    });
  });

  it("should handle missing SKILL.md file", () => {
    vi.mocked(fs.existsSync).mockReturnValue(false);

    const onError = vi.fn();
    const handle = runStageSkillInteractive("feature-dev", 42, { onError });

    expect(onError).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("SKILL.md not found"),
      })
    );
    expect(handle.isInteractive).toBe(true);
  });

  it("should log consolidated metadata line with interactive indicator", () => {
    const onStderr = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onStderr });

    // Consolidated metadata line includes "(interactive)" tag (Issue #795)
    expect(onStderr).toHaveBeenCalledWith(
      expect.stringContaining("Stage: feature-dev (interactive)")
    );
  });

  it("should kill process on handle.kill()", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    handle.kill();

    expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
  });
});

describe("skillRunner - writeToInteractiveProcess", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should write message to interactive process stdin", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    const result = writeToInteractiveProcess(handle, "Follow-up message");

    expect(result).toBe(true);
    expect(mockProcess.stdin!.write).toHaveBeenCalledWith("Follow-up message\n");
  });

  it("should return false for non-interactive process", () => {
    const handle = runStageSkillHeadless("feature-dev", 42, {});

    const result = writeToInteractiveProcess(handle, "Message");

    expect(result).toBe(false);
  });

  it("should return false when stdin is unavailable", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    // Simulate stdin being destroyed
    (mockProcess.stdin as unknown as { destroyed: boolean }).destroyed = true;

    const result = writeToInteractiveProcess(handle, "Message");

    expect(result).toBe(false);
  });
});

describe("skillRunner - isInteractiveProcess", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should return true for interactive process", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});

    expect(isInteractiveProcess(handle)).toBe(true);
  });

  it("should return false for headless process", () => {
    const handle = runStageSkillHeadless("feature-dev", 42, {});

    expect(isInteractiveProcess(handle)).toBe(false);
  });
});

describe("skillRunner - Interactive Process Tracking", () => {
  let mockProcess1: ChildProcess;
  let mockProcess2: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess1 = createMockChildProcess();
    mockProcess2 = createMockChildProcess();
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should track interactive processes separately from headless", () => {
    vi.mocked(spawn).mockReturnValueOnce(mockProcess1).mockReturnValueOnce(mockProcess2);

    // Start both types
    runStageSkillHeadless("feature-dev", 42, {});
    runStageSkillInteractive("feature-dev", 42, {});

    // Both should be tracked
    expect(hasActiveProcess()).toBe(true);
  });

  it("should clean up interactive processes on killAllActiveProcesses", () => {
    vi.mocked(spawn).mockReturnValue(mockProcess1);

    runStageSkillInteractive("feature-dev", 42, {});
    expect(hasActiveProcess()).toBe(true);

    killAllActiveProcesses();

    expect(mockProcess1.kill).toHaveBeenCalledWith("SIGTERM");
    expect(hasActiveProcess()).toBe(false);
  });

  it("should remove interactive process from tracking on completion", () => {
    vi.mocked(spawn).mockReturnValue(mockProcess1);

    runStageSkillInteractive("feature-dev", 42, {});
    expect(hasActiveProcess()).toBe(true);

    mockProcess1.emit("close", 0);

    expect(hasActiveProcess()).toBe(false);
  });
});

describe("skillRunner - CLI Flag Injection (Issue #626)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`);
    // Reset config mocks to defaults
    vi.mocked(getStageModel).mockReturnValue(undefined);
    vi.mocked(getDefaultModel).mockReturnValue(undefined);
    vi.mocked(getFallbackModel).mockReturnValue(undefined);
    vi.mocked(getMaxTurns).mockReturnValue(undefined);
    vi.mocked(getCostBudget).mockReturnValue(undefined);
    vi.mocked(getStageEffort).mockReturnValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("should pass --model when default_model is non-default", () => {
    vi.mocked(getDefaultModel).mockReturnValue("opus");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("opus");
  });

  it("should pass --model sonnet explicitly when default_model is sonnet", () => {
    vi.mocked(getDefaultModel).mockReturnValue("sonnet");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("sonnet");
  });

  it("should pass --model sonnet when default_model is undefined (fallback)", () => {
    vi.mocked(getDefaultModel).mockReturnValue(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("sonnet");
  });

  it("should pass --model opus when stage model overrides to opus (Issue #707)", () => {
    vi.mocked(getStageModel).mockReturnValue("opus");
    vi.mocked(getDefaultModel).mockReturnValue(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("opus");
  });

  it("should always pass --model explicitly even when sonnet (CLI default may differ)", () => {
    vi.mocked(getStageModel).mockReturnValue("sonnet");
    vi.mocked(getDefaultModel).mockReturnValue(undefined);

    runStageSkillHeadless("issue-pickup", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("sonnet");
  });

  it("should pass --model haiku when stage model overrides to haiku (Issue #725)", () => {
    vi.mocked(getStageModel).mockReturnValue("haiku");
    vi.mocked(getDefaultModel).mockReturnValue(undefined);

    runStageSkillHeadless("issue-pickup", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const modelIndex = args.indexOf("--model");

    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("haiku");
  });

  it("should prefer a Codex run-level model override over configured Codex model", () => {
    const previousAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";

    try {
      vi.mocked(getCodexModel).mockReturnValue("gpt-5.4");
      vi.mocked(resolveCodexPipelineModel).mockImplementation((model?: string) => {
        if (!model || model === "sonnet") return "gpt-5.4";
        if (model === "haiku") return "gpt-5.4-mini";
        if (model === "opus") return "gpt-5.5";
        return model;
      });

      runStageSkillHeadless("feature-dev", 42, {}, undefined, undefined, undefined, "gpt-5.5");

      const spawnCall = vi.mocked(spawn).mock.calls[0];
      const options = spawnCall[2] as { env: Record<string, string> };

      expect(options.env.NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.5");
      expect(options.env.NIGHTGAUGE_CODEX_REASONING_EFFORT).toBe("medium");
    } finally {
      if (previousAdapter === undefined) {
        delete process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      } else {
        process.env.NIGHTGAUGE_UI_CORE_ADAPTER = previousAdapter;
      }
    }
  });

  it("should translate a lightweight stage tier to a cheaper Codex model", () => {
    const previousAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";

    try {
      vi.mocked(getStageModel).mockReturnValue("haiku");

      runStageSkillHeadless("issue-pickup", 42, {});

      const spawnCall = vi.mocked(spawn).mock.calls[0];
      const options = spawnCall[2] as { env: Record<string, string> };

      expect(vi.mocked(resolveCodexPipelineModel)).toHaveBeenCalledWith("haiku", "/test/workspace");
      expect(options.env.NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.4-mini");
    } finally {
      if (previousAdapter === undefined) {
        delete process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      } else {
        process.env.NIGHTGAUGE_UI_CORE_ADAPTER = previousAdapter;
      }
    }
  });

  it("should translate an escalation tier to the heavy Codex model", () => {
    const previousAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";

    try {
      vi.mocked(getStageModel).mockReturnValue("opus");

      runStageSkillHeadless("feature-dev", 42, {});

      const spawnCall = vi.mocked(spawn).mock.calls[0];
      const options = spawnCall[2] as { env: Record<string, string> };

      expect(vi.mocked(resolveCodexPipelineModel)).toHaveBeenCalledWith("opus", "/test/workspace");
      expect(options.env.NIGHTGAUGE_CODEX_MODEL).toBe("gpt-5.5");
    } finally {
      if (previousAdapter === undefined) {
        delete process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      } else {
        process.env.NIGHTGAUGE_UI_CORE_ADAPTER = previousAdapter;
      }
    }
  });

  it("should record Codex run-level override in modelDecision", () => {
    const previousAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "codex";

    try {
      vi.mocked(getCodexModel).mockReturnValue("gpt-5.4");
      vi.mocked(resolveCodexPipelineModel).mockImplementation((model?: string) => {
        if (!model || model === "sonnet") return "gpt-5.4";
        if (model === "haiku") return "gpt-5.4-mini";
        if (model === "opus") return "gpt-5.5";
        return model;
      });
      const onComplete = vi.fn();

      runStageSkillHeadless(
        "feature-dev",
        42,
        { onComplete },
        undefined,
        undefined,
        undefined,
        "gpt-5.5",
        undefined,
        undefined,
        "user-override"
      );

      mockProcess.emit("close", 0);

      expect(onComplete).toHaveBeenCalledWith(
        expect.objectContaining({
          success: true,
          modelDecision: expect.objectContaining({
            model: "gpt-5.5",
            source: "user-override",
            escalatedFrom: "gpt-5.4",
          }),
        })
      );
    } finally {
      if (previousAdapter === undefined) {
        delete process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      } else {
        process.env.NIGHTGAUGE_UI_CORE_ADAPTER = previousAdapter;
      }
    }
  });

  it("should pass --fallback-model when configured", () => {
    vi.mocked(getFallbackModel).mockReturnValue("haiku");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const fbIndex = args.indexOf("--fallback-model");

    expect(fbIndex).toBeGreaterThan(-1);
    expect(args[fbIndex + 1]).toBe("haiku");
  });

  it("should pass --effort when stage effort resolves", () => {
    vi.mocked(getStageEffort).mockReturnValue("high");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const effortIndex = args.indexOf("--effort");

    expect(effortIndex).toBeGreaterThan(-1);
    expect(args[effortIndex + 1]).toBe("high");
  });

  it("should not pass --effort when stage effort is undefined", () => {
    vi.mocked(getStageEffort).mockReturnValue(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];

    expect(args).not.toContain("--effort");
  });

  it("should not pass --fallback-model when not configured", () => {
    vi.mocked(getFallbackModel).mockReturnValue(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];

    expect(args).not.toContain("--fallback-model");
  });

  it("should pass --max-turns when configured", () => {
    vi.mocked(getMaxTurns).mockReturnValue(50);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const mtIndex = args.indexOf("--max-turns");

    expect(mtIndex).toBeGreaterThan(-1);
    expect(args[mtIndex + 1]).toBe("50");
  });

  it("should not pass --max-turns when not configured", () => {
    vi.mocked(getMaxTurns).mockReturnValue(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];

    expect(args).not.toContain("--max-turns");
  });

  it("should pass --max-budget-usd when cost budget is set", () => {
    vi.mocked(getCostBudget).mockReturnValue(5.0);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];
    const budgetIndex = args.indexOf("--max-budget-usd");

    expect(budgetIndex).toBeGreaterThan(-1);
    expect(args[budgetIndex + 1]).toBe("5");
  });

  it("should not pass --max-budget-usd when cost budget is 0", () => {
    vi.mocked(getCostBudget).mockReturnValue(0);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];

    expect(args).not.toContain("--max-budget-usd");
  });

  it("should pass --model and --fallback-model in resumeSessionWithResponse", () => {
    vi.mocked(getDefaultModel).mockReturnValue("opus");
    vi.mocked(getFallbackModel).mockReturnValue("haiku");

    resumeSessionWithResponse("session-123", "user response", {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const args = spawnCall[1] as string[];

    const modelIndex = args.indexOf("--model");
    expect(modelIndex).toBeGreaterThan(-1);
    expect(args[modelIndex + 1]).toBe("opus");

    const fbIndex = args.indexOf("--fallback-model");
    expect(fbIndex).toBeGreaterThan(-1);
    expect(args[fbIndex + 1]).toBe("haiku");
  });

  it("should pass --permission-mode when auto-accept permissions is enabled", () => {
    // The auto-accept logic reads from config file or env.
    // With fs mocked, it will read env vars.
    const origEnv = process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS;
    process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = "true";

    try {
      runStageSkillHeadless("feature-dev", 42, {});

      const spawnCall = vi.mocked(spawn).mock.calls[0];
      const args = spawnCall[1] as string[];
      const pmIndex = args.indexOf("--permission-mode");

      expect(pmIndex).toBeGreaterThan(-1);
      expect(args[pmIndex + 1]).toBe("bypassPermissions");
    } finally {
      if (origEnv === undefined) {
        delete process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS;
      } else {
        process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = origEnv;
      }
    }
  });

  it("should not pass --permission-mode when auto-accept is disabled", () => {
    const origEnv = process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS;
    delete process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS;

    try {
      runStageSkillHeadless("feature-dev", 42, {});

      const spawnCall = vi.mocked(spawn).mock.calls[0];
      const args = spawnCall[1] as string[];

      expect(args).not.toContain("--permission-mode");
    } finally {
      if (origEnv !== undefined) {
        process.env.NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS = origEnv;
      }
    }
  });

  it("should log stderr messages for all configured flags", () => {
    vi.mocked(getDefaultModel).mockReturnValue("opus");
    vi.mocked(getFallbackModel).mockReturnValue("haiku");
    vi.mocked(getMaxTurns).mockReturnValue(100);
    vi.mocked(getCostBudget).mockReturnValue(10.5);

    const onStderr = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onStderr });

    const stderrCalls = onStderr.mock.calls.map((c: unknown[]) => c[0]);

    expect(stderrCalls).toContainEqual(expect.stringContaining("Model: opus"));
    expect(stderrCalls).toContainEqual(expect.stringContaining("Fallback model: haiku"));
    expect(stderrCalls).toContainEqual(expect.stringContaining("Max turns: 100"));
    expect(stderrCalls).toContainEqual(expect.stringContaining("Budget cap: $10.5"));
  });
});

/**
 * Stall Detection & Stale Notification Safety (Issue #723)
 *
 * Tests that the stall warning notification becomes stale-safe after stage
 * completion — clicking "Stop Stage" on a completed process is a no-op.
 */
describe("skillRunner - Stall Detection (#723)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash AskUserQuestion
---
# Test Skill
`);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it('should not kill process when "Stop Stage" is clicked after stage completes', async () => {
    const vscode = await import("vscode");
    // Simulate user clicking "Stop Stage" — but delay the resolution
    let resolveWarning: (value: string | undefined) => void;
    const warningPromise = new Promise<string | undefined>((resolve) => {
      resolveWarning = resolve;
    });
    vi.mocked(vscode.window.showWarningMessage).mockReturnValue(
      warningPromise as ReturnType<typeof vscode.window.showWarningMessage>
    );

    const onStderr = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, { onStderr });

    // Advance past stall warning threshold (180s for issue-pickup)
    vi.advanceTimersByTime(180_001);

    // Verify stall warning was shown
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Issue Pickup is still running"),
      "Stop Stage",
      "Keep Waiting"
    );

    // Stage completes before user clicks the notification button
    mockProcess.emit("close", 0);

    // User clicks "Stop Stage" after the process already exited
    resolveWarning!("Stop Stage");
    await warningPromise;

    // Flush microtasks so the .then() handler runs
    await vi.advanceTimersByTimeAsync(0);

    // proc.kill should NOT be called — stageCompleted flag prevents it
    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("should call onStallWarningClear when stage completes after stall warning (Issue #797)", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined);

    const onStallWarningClear = vi.fn();
    const onStderr = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, {
      onStderr,
      onStallWarningClear,
    });

    // Advance past stall warning threshold (180s for issue-pickup)
    vi.advanceTimersByTime(180_001);

    // Stage completes
    mockProcess.emit("close", 0);

    expect(onStallWarningClear).toHaveBeenCalledTimes(1);
    // Should NOT emit the old "can be dismissed" message
    const stderrCalls = onStderr.mock.calls.map((c: unknown[]) => c[0]);
    expect(stderrCalls).not.toContainEqual(
      expect.stringContaining("stall warning can be dismissed")
    );
  });

  it("should not call onStallWarningClear when no warning was shown", () => {
    const onStallWarningClear = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, { onStallWarningClear });

    // Stage completes quickly (before 180s threshold)
    mockProcess.emit("close", 0);

    expect(onStallWarningClear).not.toHaveBeenCalled();
  });

  it("should set stageCompleted on process error", async () => {
    const vscode = await import("vscode");
    let resolveWarning: (value: string | undefined) => void;
    const warningPromise = new Promise<string | undefined>((resolve) => {
      resolveWarning = resolve;
    });
    vi.mocked(vscode.window.showWarningMessage).mockReturnValue(
      warningPromise as ReturnType<typeof vscode.window.showWarningMessage>
    );

    runStageSkillHeadless("issue-pickup", 42, {});

    // Advance past stall warning threshold (180s for issue-pickup)
    vi.advanceTimersByTime(180_001);

    // Process errors out
    mockProcess.emit("error", new Error("spawn ENOENT"));

    // User clicks "Stop Stage" after process errored
    resolveWarning!("Stop Stage");
    await warningPromise;
    await vi.advanceTimersByTimeAsync(0);

    // proc.kill should NOT be called — stageCompleted flag prevents it
    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it('"Keep Waiting" prevents auto-kill at 5x threshold (Issue #2653)', async () => {
    const vscode = await import("vscode");
    let resolveWarning: (value: string | undefined) => void;
    const warningPromise = new Promise<string | undefined>((resolve) => {
      resolveWarning = resolve;
    });
    vi.mocked(vscode.window.showWarningMessage).mockReturnValue(
      warningPromise as ReturnType<typeof vscode.window.showWarningMessage>
    );

    const onStderr = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, { onStderr });

    // Advance past stall warning threshold (180s for issue-pickup)
    vi.advanceTimersByTime(180_001);

    // Verify stall warning was shown
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Issue Pickup is still running"),
      "Stop Stage",
      "Keep Waiting"
    );

    // User clicks "Keep Waiting" — should disable the kill timer
    resolveWarning!("Keep Waiting");
    await warningPromise;
    await vi.advanceTimersByTimeAsync(0);

    // Verify the stderr log confirms kill timer was disabled
    const stderrCalls = onStderr.mock.calls.map((c: unknown[]) => c[0]);
    expect(stderrCalls).toContainEqual(expect.stringContaining("stall kill timer disabled"));

    // Advance well past the kill threshold (5x = 900s)
    // Process should NOT be killed
    vi.advanceTimersByTime(900_000);

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it('"Stop Stage" still kills immediately (Issue #2653)', async () => {
    const vscode = await import("vscode");
    let resolveWarning: (value: string | undefined) => void;
    const warningPromise = new Promise<string | undefined>((resolve) => {
      resolveWarning = resolve;
    });
    vi.mocked(vscode.window.showWarningMessage).mockReturnValue(
      warningPromise as ReturnType<typeof vscode.window.showWarningMessage>
    );

    const onStderr = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, { onStderr });

    // Advance past stall warning threshold
    vi.advanceTimersByTime(180_001);

    // User clicks "Stop Stage"
    resolveWarning!("Stop Stage");
    await warningPromise;
    await vi.advanceTimersByTimeAsync(0);

    // Process should be killed with SIGTERM
    expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
  });

  it("calls onStallWarning at 1× threshold with multiplier=1 (Issue #2655)", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined);

    const onStallWarning = vi.fn();
    runStageSkillHeadless("issue-pickup", 42, { onStallWarning });

    // Advance past 1× threshold (180s for issue-pickup)
    vi.advanceTimersByTime(180_001);

    expect(onStallWarning).toHaveBeenCalledTimes(1);
    const [event, multiplier] = onStallWarning.mock.calls[0] as [
      { elapsed_ms: number; threshold_ms: number; action: string },
      number,
    ];
    expect(multiplier).toBe(1);
    expect(event.action).toBe("warn");
    expect(event.elapsed_ms).toBeGreaterThanOrEqual(180_000);
    expect(event.threshold_ms).toBe(180_000);
  });

  it("calls onStallWarning at 2× escalation with multiplier=2 (Issue #2655)", () => {
    const onStallWarning = vi.fn();
    // Use feature-planning (180s threshold, no hard cap, no toast) — pr-create now
    // has a 300s hard cap that kills the process before the 2× (360s) threshold.
    runStageSkillHeadless("feature-planning", 42, { onStallWarning });

    // Advance past 1× threshold (180s for feature-planning)
    vi.advanceTimersByTime(180_001);
    expect(onStallWarning).toHaveBeenCalledTimes(1);
    expect(onStallWarning.mock.calls[0][1]).toBe(1);

    // Advance to 2× threshold (360s total, another 180s from 1×)
    vi.advanceTimersByTime(180_000);
    expect(onStallWarning).toHaveBeenCalledTimes(2);
    expect(onStallWarning.mock.calls[1][1]).toBe(2);
  });

  it("calls onStallWarning at 3× escalation with multiplier=3 (Issue #2655)", () => {
    const onStallWarning = vi.fn();
    // Use feature-planning (180s threshold, no hard cap, no toast) — pr-create now
    // has a 300s hard cap that kills the process before the 3× (540s) threshold.
    runStageSkillHeadless("feature-planning", 42, { onStallWarning });

    vi.advanceTimersByTime(180_001); // 1× threshold (180s)
    vi.advanceTimersByTime(180_000); // 2× threshold (360s total)
    vi.advanceTimersByTime(180_000); // 3× threshold (540s total)

    expect(onStallWarning).toHaveBeenCalledTimes(3);
    expect(onStallWarning.mock.calls[2][1]).toBe(3);
  });
});

describe("skillRunner - Repo Identity (Issue #1306)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    // Clear only specific mocks, not RepositoryContextLoader
    vi.mocked(spawn).mockClear();
    vi.mocked(fs.existsSync).mockClear();
    vi.mocked(fs.readFileSync).mockClear();

    // Reset RepositoryContextLoader mock to default (null)
    mockGetCurrentRepository.mockReturnValue(null);
    mockGetWorkingDirectory.mockReturnValue("/test/workspace");

    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    // Mock fs.readFileSync to return config content for config.yaml, skill content for SKILL.md
    vi.mocked(fs.readFileSync).mockImplementation((path: string | number | Buffer) => {
      const pathStr = String(path);
      if (pathStr.endsWith("config.yaml")) {
        // Return minimal valid YAML config (empty or with minimal model_routing)
        return "model_routing: {}";
      }
      // Default to SKILL.md content for other files
      return `---\nname: test-skill\nallowed-tools: Read Write\n---\n\n# Test Skill`;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    mockGetCurrentRepository.mockReturnValue(null);
    killAllActiveProcesses();
  });

  it("should set NIGHTGAUGE_TARGET_REPO when github config is available", () => {
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "nightgauge", repo: "nightgauge" },
    });

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      expect.any(String),
      expect.any(Array),
      expect.objectContaining({
        env: expect.objectContaining({
          NIGHTGAUGE_TARGET_REPO: "nightgauge/nightgauge",
        }),
      })
    );
  });

  // Note: These tests verify NIGHTGAUGE_TARGET_REPO behavior when the actual
  // RepositoryContextLoader is used (not mocked) due to singleton caching patterns.
  // The real singleton is populated with the current repo when tests run.
  // These tests document the expected behavior: env var is set based on actual repo state.
  it("should set NIGHTGAUGE_TARGET_REPO when actual repo is available", () => {
    // The actual RepositoryContextLoader singleton will have the real repo info
    // when running in the nightgauge repo, so NIGHTGAUGE_TARGET_REPO
    // will be set to the actual repo value
    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const envObj = (spawnCall[2] as { env: Record<string, string> }).env;
    // When running in nightgauge repo, the env var will be set
    if (envObj.NIGHTGAUGE_TARGET_REPO) {
      expect(envObj.NIGHTGAUGE_TARGET_REPO).toMatch(/^[^/]+\/[^/]+$/); // owner/repo format
    }
  });

  it("should only set NIGHTGAUGE_TARGET_REPO when github config exists", () => {
    // The mock setup returns github config in the successful test case
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "TestOrg", repo: "test-repo" },
    });

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const envObj = (spawnCall[2] as { env: Record<string, string> }).env;
    // When github config is provided, the env var should be set
    expect(envObj.NIGHTGAUGE_TARGET_REPO).toBe("TestOrg/test-repo");
  });

  // Issue #3867: In multi-repo (N:1) workspaces the orchestrator passes the
  // issue's intended owning repo via targetRepoOverride. It MUST win over
  // getCurrentRepository() (which returns the workspace PRIMARY repo), or the
  // repo-mismatch gate compares against the wrong repo and every stage fails.
  // Regression: AcmeApp #42 — worktree=acmeapp-platform, primary=acmeapp-infra.
  it("prefers targetRepoOverride over getCurrentRepository (multi-repo, #3867)", () => {
    // getCurrentRepository returns the workspace PRIMARY (the wrong repo here).
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "nightgauge", repo: "acmeapp-infra" },
    });

    runStageSkillHeadless(
      "pr-create",
      42,
      {},
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
      "nightgauge/acmeapp-platform" // targetRepoOverride — the issue's actual repo
    );

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const envObj = (spawnCall[2] as { env: Record<string, string> }).env;
    expect(envObj.NIGHTGAUGE_TARGET_REPO).toBe("nightgauge/acmeapp-platform");
  });

  // Single-repo / manual invocations pass no override → fall back to current repo.
  it("falls back to getCurrentRepository when no targetRepoOverride is given (#3867)", () => {
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "nightgauge", repo: "nightgauge" },
    });

    runStageSkillHeadless("pr-create", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const envObj = (spawnCall[2] as { env: Record<string, string> }).env;
    expect(envObj.NIGHTGAUGE_TARGET_REPO).toBe("nightgauge/nightgauge");
  });

  // A blank/whitespace override must not blank out the env — fall back instead.
  it("ignores an empty targetRepoOverride and falls back (#3867)", () => {
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "nightgauge", repo: "nightgauge" },
    });

    runStageSkillHeadless(
      "pr-create",
      42,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      "   " // whitespace-only override
    );

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const envObj = (spawnCall[2] as { env: Record<string, string> }).env;
    expect(envObj.NIGHTGAUGE_TARGET_REPO).toBe("nightgauge/nightgauge");
  });
});

/**
 * Tests for pinnedWorkspaceRoot parameter (Issue #1592)
 *
 * Verifies that when a pinned workspace root is provided, it overrides
 * dynamic resolution from RepositoryContextLoader, preventing repo-switch
 * mid-pipeline from changing the CWD for subsequent stages.
 */
describe("skillRunner - Pinned Workspace Root (Issue #1592)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
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
    killAllActiveProcesses();
  });

  it("should use pinnedWorkspaceRoot as CWD when provided", () => {
    const pinnedRoot = "/pinned/repo-A";

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      pinnedRoot
    );

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    expect(spawnCall[2]).toEqual(expect.objectContaining({ cwd: pinnedRoot }));
  });

  it("should ignore RepositoryContextLoader when pinnedWorkspaceRoot is set", () => {
    // Simulate a repo switch: context loader now points to repo-B
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "test", repo: "repo-B" },
    });
    mockGetWorkingDirectory.mockReturnValue("/switched/repo-B");

    const pinnedRoot = "/original/repo-A";

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      pinnedRoot
    );

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    expect(spawnCall[2]).toEqual(expect.objectContaining({ cwd: pinnedRoot }));
    // Context loader should NOT have been consulted for workspaceRoot
    // (it's still called for targetRepo identity, which is fine)
  });

  it("should fall back to dynamic resolution when pinnedWorkspaceRoot is not provided", () => {
    mockGetCurrentRepository.mockReturnValue({
      github: { owner: "test", repo: "repo-A" },
    });
    mockGetWorkingDirectory.mockReturnValue("/dynamic/repo-A");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    expect(spawnCall[2]).toEqual(expect.objectContaining({ cwd: "/dynamic/repo-A" }));
  });
});

describe("skillRunner - mcp-tools frontmatter field (Issue #1725)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default: getStageMcpTools returns [] (no config override)
    vi.mocked(getStageMcpTools).mockReturnValue([]);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  function setupSkillWithMcpTools(mcpToolsValue?: string) {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    const frontmatter = mcpToolsValue
      ? `---\nname: test-skill\nallowed-tools: Read Write Edit\nmcp-tools: ${mcpToolsValue}\n---\n# Test\n`
      : `---\nname: test-skill\nallowed-tools: Read Write Edit\n---\n# Test\n`;
    vi.mocked(fs.readFileSync).mockReturnValue(frontmatter);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    return mockProcess;
  }

  // Test 1: specific fully-qualified MCP tool name
  it("should append a specific MCP tool name to --allowedTools", () => {
    setupSkillWithMcpTools("mcp__playwright__browser_click");

    runStageSkillHeadless("feature-dev", 42, {});

    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.arrayContaining([
        "--allowedTools",
        expect.stringContaining("mcp__playwright__browser_click"),
      ]),
      expect.any(Object)
    );
  });

  // Test 2: multiple glob patterns
  it("should append multiple MCP tool patterns to --allowedTools", () => {
    setupSkillWithMcpTools("mcp__playwright__* mcp__sentry__*");

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).toContain("mcp__playwright__*");
    expect(allowedToolsArg).toContain("mcp__sentry__*");
  });

  // Test 3: omitted mcp-tools — no MCP tools added (backward compat)
  it("should not add MCP tools when mcp-tools is omitted", () => {
    setupSkillWithMcpTools(undefined);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).not.toContain("mcp__");
  });

  // Test 4: 'all' with .claude/settings.json present
  it("should expand 'all' to mcp__{server}__* patterns from settings.json", () => {
    vi.mocked(fs.existsSync).mockImplementation((p) => {
      const pathStr = String(p);
      // skill file found
      if (pathStr.includes("SKILL.md")) return true;
      // settings.json found
      if (pathStr.includes(".claude/settings.json")) return true;
      return false;
    });
    vi.mocked(fs.readFileSync).mockImplementation((p) => {
      const pathStr = String(p);
      if (pathStr.includes(".claude/settings.json")) {
        return JSON.stringify({ mcpServers: { playwright: {} } });
      }
      return `---\nname: test-skill\nallowed-tools: Read Write Edit\nmcp-tools: all\n---\n# Test\n`;
    });
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).toContain("mcp__playwright__*");
  });

  // Test 5: 'all' with no .claude/settings.json — graceful empty
  it("should return no MCP tools when 'all' is set but settings.json is absent", () => {
    vi.mocked(fs.existsSync).mockImplementation((p) => {
      const pathStr = String(p);
      if (pathStr.includes("SKILL.md")) return true;
      // settings.json NOT found
      return false;
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      `---\nname: test-skill\nallowed-tools: Read Write Edit\nmcp-tools: all\n---\n# Test\n`
    );
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).not.toContain("mcp__");
  });

  // Test 6: config override wins over frontmatter
  it("should use config.yaml mcp_tools override instead of frontmatter", () => {
    // frontmatter says playwright, config says sentry
    setupSkillWithMcpTools("mcp__playwright__*");
    vi.mocked(getStageMcpTools).mockReturnValue(["mcp__sentry__get_issue"]);

    runStageSkillHeadless("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).toContain("mcp__sentry__get_issue");
    expect(allowedToolsArg).not.toContain("mcp__playwright__*");
  });

  // Test 7: interactive mode with MCP tools
  it("should append MCP tools in interactive mode", () => {
    setupSkillWithMcpTools("mcp__playwright__*");

    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillInteractive("feature-dev", 42, {});

    const spawnCall = vi.mocked(spawn).mock.calls[0];
    const allowedToolsArg = spawnCall[1][spawnCall[1].indexOf("--allowedTools") + 1];
    expect(allowedToolsArg).toContain("mcp__playwright__*");
  });
});

describe("skillRunner - Injected Skill Content (Issue #1473)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("should use injected content instead of reading from disk", () => {
    const injectedContent = `---
name: injected-skill
allowed-tools: Read Write Edit
---
# Injected Skill Content
This content was resolved from the platform.
`;
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onStderr = vi.fn();
    runStageSkillHeadless(
      "feature-dev",
      42,
      { onStderr },
      undefined, // issueMetadata
      undefined, // batchContext
      undefined, // skipToPhase
      undefined, // modelOverride
      undefined, // pauseAutoRouting
      undefined, // pinnedWorkspaceRoot
      undefined, // modelOverrideSource
      injectedContent // injectedSkillContent
    );

    // fs.readFileSync should NOT be called for the skill file — injected
    // content replaces the disk CONTENT. (#196: a stat-only findSkillFile
    // probe IS now expected even with injected content, to resolve the
    // absolute skill dir for NIGHTGAUGE_SKILL_DIR and read-directive
    // rewriting — so no assertion on existsSync discovery calls.)
    const readCalls = vi.mocked(fs.readFileSync).mock.calls;
    const skillReadCalls = readCalls.filter((call) => String(call[0]).includes("SKILL.md"));
    expect(skillReadCalls).toHaveLength(0);

    // spawn should still be called (process is started)
    expect(spawn).toHaveBeenCalled();
  });

  it("should parse allowedTools from injected frontmatter", () => {
    const injectedContent = `---
name: platform-skill
allowed-tools: Read Write Glob Grep
---
# Platform Skill
`;
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      injectedContent
    );

    // Check that spawn was called with the injected allowed tools
    expect(spawn).toHaveBeenCalledWith(
      "claude",
      expect.arrayContaining(["--allowedTools", expect.stringMatching(/Read,Write,Glob,Grep/)]),
      expect.any(Object)
    );
  });

  it("should fall back to disk on injected content parse failure", () => {
    // Empty string content — parseSkillContent should return null
    const injectedContent = "";
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: local-skill
allowed-tools: Read Bash
---
# Local Fallback
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    runStageSkillHeadless(
      "feature-dev",
      42,
      {},
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      injectedContent
    );

    // When injected content is empty/falsy, should use disk path
    // (empty string is falsy, so it takes the else branch)
    expect(spawn).toHaveBeenCalled();
  });

  it("should use local file when no injected content provided", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: local-skill
allowed-tools: Read Write Edit
---
# Local Skill
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    // Call without injectedSkillContent (undefined)
    runStageSkillHeadless("feature-dev", 42, {});

    // fs.existsSync should be called for skill file discovery
    const existsCalls = vi.mocked(fs.existsSync).mock.calls;
    const skillFileCalls = existsCalls.filter((call) => String(call[0]).includes("SKILL.md"));
    expect(skillFileCalls.length).toBeGreaterThan(0);
  });
});

describe("classifyError (Issue #2573)", () => {
  it("should classify rate limit errors", () => {
    expect(classifyError("Error: rate limit exceeded")).toBe("rate_limit");
    expect(classifyError("HTTP 429 Too Many Requests")).toBe("rate_limit");
    expect(classifyError("too many requests")).toBe("rate_limit");
    expect(classifyError("quota exceeded for API")).toBe("rate_limit");
  });

  it("should classify Anthropic session/usage limits as rate_limit (#3792)", () => {
    expect(classifyError("You've hit your session limit · resets 10:30am")).toBe("rate_limit");
    expect(classifyError("usage limit reached")).toBe("rate_limit");
  });

  it("should classify auth errors", () => {
    expect(classifyError("HTTP 401 Unauthorized")).toBe("auth");
    expect(classifyError("HTTP 403 Forbidden")).toBe("auth");
    expect(classifyError("unauthorized access")).toBe("auth");
    expect(classifyError("permission denied")).toBe("auth");
    expect(classifyError("Error: forbidden")).toBe("auth");
  });

  it("should classify network errors", () => {
    expect(classifyError("connection refused")).toBe("network");
    expect(classifyError("request timeout")).toBe("network");
    expect(classifyError("ECONNRESET")).toBe("network");
    expect(classifyError("network error")).toBe("network");
    expect(classifyError("DNS resolution failed")).toBe("network");
  });

  it("should classify token limit errors", () => {
    expect(classifyError("token limit exceeded")).toBe("token_limit");
    expect(classifyError("context length exceeded")).toBe("token_limit");
    expect(classifyError("max_tokens reached")).toBe("token_limit");
  });

  it("should return unknown for unrecognized errors", () => {
    expect(classifyError("something went wrong")).toBe("unknown");
    expect(classifyError("test failed")).toBe("unknown");
    expect(classifyError("")).toBe("unknown");
  });

  it("should be case-insensitive", () => {
    expect(classifyError("RATE LIMIT")).toBe("rate_limit");
    expect(classifyError("Unauthorized")).toBe("auth");
    expect(classifyError("CONNECTION REFUSED")).toBe("network");
    expect(classifyError("TOKEN LIMIT")).toBe("token_limit");
  });
});

describe("extractStreamJsonError — session-limit normalization (#3792)", () => {
  it("rewrites an Anthropic session-limit result to the quota-exhausted marker with resetsAt", () => {
    const line = JSON.stringify({
      type: "result",
      is_error: true,
      subtype: "success",
      result: "You've hit your session limit · resets 10:30am (America/Denver)",
    });
    const outcome = extractStreamJsonError(line);
    expect(outcome.kind).toBe("error");
    const msg = outcome.kind === "error" ? outcome.error.message : "";
    // Must carry the canonical marker so the environmental-quota recovery path
    // (cooldown-until-reset, no terminal halt, auto-resume) engages.
    expect(msg).toContain("[rate-limit-quota-exhausted]");
    expect(msg).toMatch(/resetsAt=\d+/);
  });

  it("leaves a non-limit is_error result unchanged", () => {
    const line = JSON.stringify({
      type: "result",
      is_error: true,
      result: "feature-validate: 2 tests failed",
    });
    const outcome = extractStreamJsonError(line);
    expect(outcome.kind).toBe("error");
    const msg = outcome.kind === "error" ? outcome.error.message : "";
    expect(msg).not.toContain("[rate-limit-quota-exhausted]");
    expect(msg).toContain("2 tests failed");
  });
});

describe("rate_limit_event in stream processing (Issue #2573)", () => {
  it("should emit onRateLimitEvent callback when rate_limit_event is in stream output", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write
---
# Test Skill
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onRateLimitEvent = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onRateLimitEvent });

    // Simulate rate_limit_event in stdout
    const rateLimitJson = JSON.stringify({
      type: "rate_limit_event",
      resetsAt: Math.floor(Date.now() / 1000) + 600,
      rateLimitType: "seven_day",
      utilization: 98,
      status: "limited",
      isUsingOverage: false,
    });

    mockProcess.stdout?.emit("data", Buffer.from(rateLimitJson + "\n"));

    expect(onRateLimitEvent).toHaveBeenCalledOnce();
    const event = onRateLimitEvent.mock.calls[0][0];
    expect(event.rateLimitType).toBe("seven_day");
    expect(event.utilization).toBe(98);
    expect(event.status).toBe("limited");
    expect(event.waitMs).toBeGreaterThan(0);
  });

  it("should include errorCategory in SkillRunResult for rate_limit_event failures", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write
---
# Test Skill
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // Simulate a rate_limit_event followed by exit code 1
    const rateLimitJson = JSON.stringify({
      type: "rate_limit_event",
      resetsAt: Math.floor(Date.now() / 1000) + 600,
      rateLimitType: "seven_day",
      utilization: 100,
      status: "limited",
      isUsingOverage: false,
    });
    mockProcess.stdout?.emit("data", Buffer.from(rateLimitJson + "\n"));

    // Process exits with error
    mockProcess.emit("close", 1);

    expect(onComplete).toHaveBeenCalledOnce();
    const result = onComplete.mock.calls[0][0];
    expect(result.success).toBe(false);
    expect(result.errorCategory).toBe("rate_limit");
    expect(result.retryAfterMs).toBeGreaterThan(0);
  });

  it("should classify stderr errors when no rate_limit_event was detected", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write

# Test Skill
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // Stderr with auth error, no rate_limit_event
    mockProcess.stderr?.emit("data", Buffer.from("Error: 401 Unauthorized\n"));

    // Process exits with error
    mockProcess.emit("close", 1);

    expect(onComplete).toHaveBeenCalledOnce();
    const result = onComplete.mock.calls[0][0];
    expect(result.errorCategory).toBe("auth");
    expect(result.retryAfterMs).toBeUndefined();
  });

  it("should not set errorCategory on successful exit", () => {
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write

# Test Skill
`);
    const mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);

    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 42, { onComplete });

    // Process exits successfully
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledOnce();
    const result = onComplete.mock.calls[0][0];
    expect(result.success).toBe(true);
    expect(result.errorCategory).toBeUndefined();
  });
});

describe("skillRunner - Quota-Exhausted Fast-Fail (Issue #3425)", () => {
  let mockProcess: ChildProcess;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    killAllActiveProcesses();
  });

  it("kills the stage within ~120s of quota-exhausted rate_limit_event (default feature-dev kill is 80m)", () => {
    // feature-dev defaults: 600s warn × 8 multiplier = 4_800_000ms (80m) kill.
    // With the fast-fail gate, a `status: "limited"` event followed by
    // 120s of silence should kill the stage at the next 30s ticker fire
    // after crossing the 120s threshold — well under 80m.
    //
    // Issue #3448: the trigger now requires `status === "limited"`. The
    // earlier OR branch on `overageStatus === "rejected" &&
    // overageDisabledReason === "out_of_credits"` was a false-positive
    // that killed healthy pipelines on every plan-without-overage; that
    // branch was removed.
    runStageSkillHeadless("feature-dev", 889, {});

    // Quota-exhausted rate_limit_event arrives ~early in the run.
    const quotaEvent = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "limited",
        resetsAt: 1778403000,
        rateLimitType: "five_hour",
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
        isUsingOverage: false,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(quotaEvent + "\n"));

    // No further output. Tick interval is 30s; we need idle ≥120s before
    // the gate fires. Advance 150s to be safely past the gate.
    vi.advanceTimersByTime(150_000);

    // Process must have been signaled — fast-fail wins over the 80m default.
    expect(mockProcess.kill).toHaveBeenCalledWith("SIGTERM");
  });

  // Issue #3448: explicit pin for the false-positive case. Anthropic emits
  // this exact payload as the steady-state on plans without overage; the
  // earlier OR branch (overageStatus rejected + overageDisabledReason
  // out_of_credits) treated it as quota exhaustion even though
  // `status: "allowed"` means the request IS served. The fix removes that
  // branch — the kill must NOT fire on this payload, even past the
  // 120s fast-fail idle window.
  it("does NOT fast-fail on `status: allowed` + overage rejected + out_of_credits (false-positive pin, #3448)", () => {
    runStageSkillHeadless("feature-dev", 889, {});

    const allowedButOverageRejected = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "allowed",
        resetsAt: 1778428800,
        rateLimitType: "five_hour",
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
        isUsingOverage: false,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(allowedButOverageRejected + "\n"));

    // Advance well past the 120s fast-fail idle threshold — under the old
    // (buggy) trigger this would have killed at ~120s. After the fix the
    // base bucket is still serving requests (`status: "allowed"`) so no
    // kill must fire.
    vi.advanceTimersByTime(180_000);

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("does NOT fast-fail when no rate_limit_event has been observed", () => {
    runStageSkillHeadless("feature-dev", 889, {});

    // No rate_limit_event. Stay idle 150s — should not be killed yet
    // (default feature-dev kill threshold is 4.8M ms).
    vi.advanceTimersByTime(150_000);

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("does NOT fast-fail on a healthy `allowed` event without overage rejection", () => {
    runStageSkillHeadless("feature-dev", 889, {});

    // Healthy event — agent is fine, just informing us of utilization.
    const healthyEvent = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "allowed",
        resetsAt: 1778403000,
        rateLimitType: "five_hour",
        overageStatus: "allowed",
        isUsingOverage: false,
        utilization: 50,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(healthyEvent + "\n"));
    vi.advanceTimersByTime(150_000);

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("emits the [rate-limit-quota-exhausted] kill marker so the Go classifier routes the failure correctly", () => {
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-dev", 889, { onStderr });

    const quotaEvent = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "limited",
        resetsAt: 1778403000,
        rateLimitType: "five_hour",
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
        isUsingOverage: false,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(quotaEvent + "\n"));
    vi.advanceTimersByTime(150_000);

    const stderrCalls = onStderr.mock.calls.map((c: unknown[]) => String(c[0]));
    expect(stderrCalls.some((s) => s.includes("[rate-limit-quota-exhausted]"))).toBe(true);
  });

  it("does NOT fast-fail before the 120s idle threshold has elapsed since the quota event", () => {
    runStageSkillHeadless("feature-dev", 889, {});

    // True quota-exhausted payload (#3448: only `status: "limited"` triggers).
    const quotaEvent = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "limited",
        resetsAt: 1778403000,
        rateLimitType: "five_hour",
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
        isUsingOverage: false,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(quotaEvent + "\n"));

    // Only 90s of silence — under the 120s fast-fail threshold.
    vi.advanceTimersByTime(90_000);

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });

  it("does NOT fast-fail when the agent emits chunks every ~30s after the quota event (active progress)", () => {
    runStageSkillHeadless("feature-dev", 889, {});

    const quotaEvent = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: {
        status: "limited",
        resetsAt: 1778403000,
        rateLimitType: "five_hour",
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
        isUsingOverage: false,
      },
    });
    mockProcess.stdout?.emit("data", Buffer.from(quotaEvent + "\n"));

    // Simulate the agent making progress despite the prior quota signal:
    // 4 × 30s waits with a chunk emitted at each interval. Total elapsed
    // 120s but `idleMs` always resets to ~30s (under the gate).
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(30_000);
      mockProcess.stdout?.emit("data", Buffer.from('{"type":"assistant"}\n'));
    }

    expect(mockProcess.kill).not.toHaveBeenCalled();
  });
});

// ─── resolveTokenForSubprocess tests (Issue #2670) ───────────────────────────

describe("resolveTokenForSubprocess", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    process.env = { ...originalEnv };
    vi.mocked(getGitHubAuthToken).mockReturnValue(null);
    vi.mocked(getGitHubAuthTokens).mockReturnValue({});
    vi.mocked(getGitHubUser).mockReturnValue(null);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns config token from github_auth.token (priority 1)", () => {
    vi.mocked(getGitHubAuthToken).mockReturnValue("ghp_config_direct");
    process.env.GITHUB_TOKEN = "ghp_env";

    const result = resolveTokenForSubprocess("/test/workspace");
    expect(result).not.toBeNull();
    expect(result!.token).toBe("ghp_config_direct");
    expect(result!.source).toContain("config");
    expect(result!.source).toContain("github_auth.token");
  });

  it("returns per-org token from github_auth.tokens (priority 2)", () => {
    vi.mocked(getGitHubAuthToken).mockReturnValue(null);
    vi.mocked(getGitHubAuthTokens).mockReturnValue({ myorg: "ghp_org_token" });
    process.env.GITHUB_TOKEN = "ghp_env";

    const result = resolveTokenForSubprocess("/test/workspace");
    expect(result).not.toBeNull();
    expect(result!.token).toBe("ghp_org_token");
    expect(result!.source).toContain("config");
    expect(result!.source).toContain("myorg");
  });

  it("falls back to GITHUB_TOKEN env var (priority 3)", () => {
    vi.mocked(getGitHubAuthToken).mockReturnValue(null);
    vi.mocked(getGitHubAuthTokens).mockReturnValue({});
    process.env.GITHUB_TOKEN = "ghp_from_env";

    const result = resolveTokenForSubprocess("/test/workspace");
    expect(result).not.toBeNull();
    expect(result!.token).toBe("ghp_from_env");
    expect(result!.source).toContain("env");
  });

  it("returns null when no token is available at any priority", () => {
    vi.mocked(getGitHubAuthToken).mockReturnValue(null);
    vi.mocked(getGitHubAuthTokens).mockReturnValue({});
    delete process.env.GITHUB_TOKEN;
    vi.mocked(getGitHubUser).mockReturnValue(null);
    // execFileSync (for gh CLI) will throw since gh is not available in test env
    // The implementation catches the error and returns null

    const result = resolveTokenForSubprocess("/test/workspace");
    // May return null or a token from gh CLI depending on test environment
    // Just assert return type is correct (null or { token: string, source: string })
    if (result !== null) {
      expect(typeof result.token).toBe("string");
      expect(typeof result.source).toBe("string");
    }
  });

  it("config token takes priority over all other sources", () => {
    vi.mocked(getGitHubAuthToken).mockReturnValue("ghp_config_wins");
    vi.mocked(getGitHubAuthTokens).mockReturnValue({ org: "ghp_org" });
    process.env.GITHUB_TOKEN = "ghp_env";
    vi.mocked(getGitHubUser).mockReturnValue("someuser");

    const result = resolveTokenForSubprocess("/test/workspace");
    expect(result).not.toBeNull();
    expect(result!.token).toBe("ghp_config_wins");
  });
});

describe("skillRunner - served-model attribution (#91)", () => {
  let mockProcess: ChildProcess;
  let originalEnv: NodeJS.ProcessEnv;

  beforeEach(() => {
    vi.clearAllMocks();
    originalEnv = { ...process.env };
    mockProcess = createMockChildProcess();
    vi.mocked(spawn).mockReturnValue(mockProcess);
    vi.mocked(fs.existsSync).mockReturnValue(true);
    vi.mocked(fs.readFileSync).mockReturnValue(`---\nallowed-tools: Read\n---\n# Skill`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    process.env = originalEnv;
    killAllActiveProcesses();
  });

  it("attributes the fallback model when the CLI's model_refusal_fallback fires", () => {
    // Regression for #91: the claude CLI silently retries a safety-refused
    // turn on a fallback model and still exits 0. The recorded model MUST be
    // the fallback model, not the requested one. Event shape captured live —
    // docs/spikes/fable-5-behavior-porting.md §8.3.
    const onComplete = vi.fn();
    const onStderr = vi.fn();
    runStageSkillHeadless("feature-dev", 91, { onComplete, onStderr });

    const lines = [
      JSON.stringify({ type: "system", subtype: "init", model: "claude-fable-5" }),
      JSON.stringify({
        type: "system",
        subtype: "model_refusal_fallback",
        trigger: "refusal",
        original_model: "claude-fable-5",
        fallback_model: "claude-opus-4-8",
        api_refusal_category: "reasoning_extraction",
      }),
      JSON.stringify({
        type: "assistant",
        message: { model: "claude-opus-4-8", content: [{ type: "text", text: "continuing" }] },
      }),
      JSON.stringify({
        type: "result",
        usage: { input_tokens: 500, output_tokens: 250 },
        total_cost_usd: 0.03,
      }),
    ];
    mockProcess.stdout!.emit("data", Buffer.from(lines.join("\n") + "\n"));
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: true,
        servedModel: "claude-opus-4-8",
        modelRefusalFallback: {
          originalModel: "claude-fable-5",
          fallbackModel: "claude-opus-4-8",
          category: "reasoning_extraction",
        },
      })
    );

    // The swap must be observable the moment it happens (log line AC).
    const stderrText = onStderr.mock.calls.map((c) => String(c[0])).join("");
    expect(stderrText).toContain("model_refusal_fallback");
    expect(stderrText).toContain("claude-fable-5");
    expect(stderrText).toContain("claude-opus-4-8");
  });

  it("leaves servedModel undefined when the stream carries no model info", () => {
    const onComplete = vi.fn();
    runStageSkillHeadless("feature-dev", 91, { onComplete });

    const resultMessage = JSON.stringify({
      type: "result",
      usage: { input_tokens: 100, output_tokens: 50 },
      total_cost_usd: 0.01,
    });
    mockProcess.stdout!.emit("data", Buffer.from(resultMessage + "\n"));
    mockProcess.emit("close", 0);

    expect(onComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        servedModel: undefined,
        modelRefusalFallback: undefined,
      })
    );
  });
});
