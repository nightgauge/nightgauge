/**
 * Unit tests for GrokSetupService.ts
 *
 * Covers autoPrompt gating, missing-CLI skip, the install prompt, and the
 * home-skills copy into a fake GROK_HOME.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { resetMainChannelForTests } from "../../src/utils/logger";

const mockExecAsync = vi.hoisted(() => vi.fn());

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      show: vi.fn(),
      appendLine: vi.fn(),
      dispose: vi.fn(),
    })),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(true),
    })),
    workspaceFolders: [] as { uri: { fsPath: string } }[],
  },
}));

vi.mock("child_process", () => ({
  exec: vi.fn(),
  execFile: vi.fn(),
}));

vi.mock("util", () => ({
  promisify: vi.fn(() => mockExecAsync),
}));

function makeMockContext(extensionPath: string, overrides?: { dismissed?: boolean }) {
  return {
    globalState: {
      get: vi.fn((key: string, defaultValue?: unknown) => {
        if (key === "nightgauge.grokSetup.dismissed") {
          return overrides?.dismissed ?? defaultValue ?? false;
        }
        if (key === "nightgauge.grokSetup.installed") {
          return defaultValue ?? false;
        }
        return defaultValue;
      }),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath,
  };
}

function grokPresent(): void {
  mockExecAsync.mockImplementation(async (cmd: unknown, args?: unknown) => {
    const command = typeof cmd === "string" ? cmd : "";
    const argv = Array.isArray(args) ? (args as string[]) : [];
    if (command.includes("--version") || argv[0] === "--version") {
      return { stdout: "grok 1.0.0", stderr: "" };
    }
    if (command.includes("plugin list") || (argv[0] === "plugin" && argv[1] === "list")) {
      return { stdout: "", stderr: "" };
    }
    return { stdout: "ok", stderr: "" };
  });
}

describe("GrokSetupService", () => {
  let tmpRoot: string;
  let extensionPath: string;
  let grokHome: string;
  let prevGrokHome: string | undefined;

  beforeEach(async () => {
    vi.clearAllMocks();
    resetMainChannelForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-grok-setup-"));
    extensionPath = path.join(tmpRoot, "extension");
    grokHome = path.join(tmpRoot, "grok-home");
    fs.mkdirSync(extensionPath, { recursive: true });
    prevGrokHome = process.env.GROK_HOME;
    process.env.GROK_HOME = grokHome;

    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn().mockReturnValue(true),
    } as never);
    (
      vscode.workspace as { workspaceFolders: typeof vscode.workspace.workspaceFolders }
    ).workspaceFolders = [];
    grokPresent();
  });

  afterEach(() => {
    if (prevGrokHome === undefined) {
      delete process.env.GROK_HOME;
    } else {
      process.env.GROK_HOME = prevGrokHome;
    }
    fs.rmSync(tmpRoot, { recursive: true, force: true });
    vi.restoreAllMocks();
  });

  it("returns early when autoPrompt config is false", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn().mockReturnValue(false),
    } as never);

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("returns silently when grok CLI is not available", async () => {
    const vscode = await import("vscode");
    mockExecAsync.mockRejectedValue(new Error("command not found: grok"));

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("shows the install prompt when grok is present and Nightgauge is not installed", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("not installed for Grok"),
      expect.objectContaining({ modal: true }),
      "Install",
      "Later",
      "Don't Show Again"
    );
  });

  it("Install copies bundled plugin skills into GROK_HOME", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);

    const pluginSkills = path.join(
      extensionPath,
      "dist",
      "claude-plugins",
      "nightgauge",
      "skills",
      "issue-create"
    );
    fs.mkdirSync(path.join(pluginSkills, "_includes"), { recursive: true });
    fs.writeFileSync(path.join(pluginSkills, "SKILL.md"), "# issue-create\n");
    fs.writeFileSync(path.join(pluginSkills, "_includes", "environment-and-content.md"), "# env\n");
    fs.mkdirSync(
      path.join(extensionPath, "dist", "claude-plugins", "nightgauge", "skills", "_shared"),
      {
        recursive: true,
      }
    );
    fs.writeFileSync(
      path.join(
        extensionPath,
        "dist",
        "claude-plugins",
        "nightgauge",
        "skills",
        "_shared",
        "PREFLIGHT.md"
      ),
      "# preflight\n"
    );
    fs.mkdirSync(path.join(extensionPath, "dist", ".claude-plugin"), { recursive: true });
    fs.writeFileSync(
      path.join(extensionPath, "dist", ".claude-plugin", "marketplace.json"),
      JSON.stringify({ name: "nightgauge-plugins" })
    );

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();

    const destSkill = path.join(grokHome, "skills", "nightgauge-issue-create", "SKILL.md");
    const destInclude = path.join(
      grokHome,
      "skills",
      "nightgauge-issue-create",
      "_includes",
      "environment-and-content.md"
    );
    const destShared = path.join(grokHome, "skills", "_shared", "PREFLIGHT.md");
    expect(fs.existsSync(destSkill)).toBe(true);
    expect(fs.existsSync(destInclude)).toBe(true);
    expect(fs.existsSync(destShared)).toBe(true);
    expect(fs.readFileSync(destSkill, "utf8")).toContain("issue-create");
  });
});
