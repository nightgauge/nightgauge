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

function makeMockContext(
  extensionPath: string,
  overrides?: { dismissed?: boolean; installedVersion?: string }
) {
  return {
    globalState: {
      get: vi.fn((key: string, defaultValue?: unknown) => {
        if (key === "nightgauge.grokSetup.dismissed") {
          return overrides?.dismissed ?? defaultValue ?? false;
        }
        if (key === "nightgauge.grokSetup.installedVersion") {
          return overrides?.installedVersion ?? defaultValue;
        }
        if (key === "nightgauge.grokSetup.installed") {
          return defaultValue ?? false;
        }
        return defaultValue;
      }),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath,
    globalStorageUri: { fsPath: path.join(path.dirname(extensionPath), "globalStorage") },
    extension: { packageJSON: { version: "2.0.0" } },
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

  it("Install copies the bundled canonical skills into GROK_HOME", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);

    const bundled = path.join(extensionPath, "dist", "skills");
    fs.mkdirSync(path.join(bundled, "nightgauge-issue-create", "_includes"), { recursive: true });
    fs.writeFileSync(path.join(bundled, "nightgauge-issue-create", "SKILL.md"), "# issue-create\n");
    fs.writeFileSync(
      path.join(bundled, "nightgauge-issue-create", "_includes", "environment-and-content.md"),
      "# env\n"
    );
    fs.mkdirSync(path.join(bundled, "_shared"), { recursive: true });
    fs.writeFileSync(path.join(bundled, "_shared", "PREFLIGHT.md"), "# preflight\n");
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

  it("registers a marketplace outside the version-keyed extension directory (#2719)", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);

    const dist = path.join(extensionPath, "dist");
    const pluginDir = path.join(dist, "claude-plugins", "nightgauge");
    fs.mkdirSync(path.join(pluginDir, "skills", "issue-create"), { recursive: true });
    fs.writeFileSync(path.join(pluginDir, "skills", "issue-create", "SKILL.md"), "# x\n");
    fs.mkdirSync(path.join(dist, ".claude-plugin"), { recursive: true });
    fs.writeFileSync(path.join(dist, ".claude-plugin", "marketplace.json"), "{}");

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();

    const stable = path.join(tmpRoot, "globalStorage", "claude-marketplace");
    expect(mockExecAsync).toHaveBeenCalledWith("grok", ["plugin", "marketplace", "add", stable]);
    expect(mockExecAsync).toHaveBeenCalledWith("grok", [
      "plugin",
      "install",
      path.join(stable, "claude-plugins", "nightgauge"),
      "--trust",
    ]);
  });

  it("hands grok the marketplace URL without the #stable ref", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);

    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    const service = new GrokSetupService(makeMockContext(extensionPath) as never);
    await service.checkAndPromptSetup();

    expect(mockExecAsync).toHaveBeenCalledWith("grok", [
      "plugin",
      "marketplace",
      "add",
      "https://github.com/nightgauge/nightgauge.git",
    ]);
  });

  describe("installed version stamp (#2762)", () => {
    const skillFile = () => path.join(grokHome, "skills", "nightgauge-issue-create", "SKILL.md");

    function bundle(content: string): void {
      const dir = path.join(extensionPath, "dist", "skills", "nightgauge-issue-create");
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, "SKILL.md"), content);
    }

    /** An install an older extension made: marked, so a re-sync may replace it. */
    function installed(content: string): void {
      const dir = path.dirname(skillFile());
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, "SKILL.md"), content);
      fs.writeFileSync(path.join(dir, ".nightgauge-installed"), "x");
    }

    function autoPrompt(value: boolean): Promise<void> {
      return import("vscode").then((vscode) => {
        vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
          get: vi.fn().mockReturnValue(value),
        } as never);
      });
    }

    it("stamps the extension version after an install", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);
      bundle("# v2\n");
      const context = makeMockContext(extensionPath);

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(context as never).checkAndPromptSetup();

      expect(fs.readFileSync(skillFile(), "utf8")).toBe("# v2\n");
      expect(context.globalState.update).toHaveBeenCalledWith(
        "nightgauge.grokSetup.installedVersion",
        "2.0.0"
      );
    });

    it("re-syncs skills another version installed, with autoPrompt off", async () => {
      const vscode = await import("vscode");
      await autoPrompt(false);
      bundle("# v2\n");
      installed("# v1\n");
      const context = makeMockContext(extensionPath, { installedVersion: "1.0.0" });

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(context as never).checkAndPromptSetup();

      expect(fs.readFileSync(skillFile(), "utf8")).toBe("# v2\n");
      expect(context.globalState.update).toHaveBeenCalledWith(
        "nightgauge.grokSetup.installedVersion",
        "2.0.0"
      );
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Nightgauge updated its Grok skills to 2.0.0."
      );
    });

    it("re-syncs an install older than the stamp itself", async () => {
      await autoPrompt(false);
      bundle("# v2\n");
      installed("# v1\n");

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(makeMockContext(extensionPath) as never).checkAndPromptSetup();

      expect(fs.readFileSync(skillFile(), "utf8")).toBe("# v2\n");
    });

    it("leaves skills this version installed alone", async () => {
      const vscode = await import("vscode");
      await autoPrompt(false);
      bundle("# v2\n");
      installed("# kept\n");
      const context = makeMockContext(extensionPath, { installedVersion: "2.0.0" });

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(context as never).checkAndPromptSetup();

      expect(fs.readFileSync(skillFile(), "utf8")).toBe("# kept\n");
      expect(context.globalState.update).not.toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("never installs unasked: no skills means no re-sync", async () => {
      await autoPrompt(false);
      bundle("# v2\n");

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(makeMockContext(extensionPath) as never).checkAndPromptSetup();

      expect(fs.existsSync(skillFile())).toBe(false);
    });
  });

  describe("showSetupPrompt() (#2762)", () => {
    it("offers an install with autoPrompt off and after Don't Show Again", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn().mockReturnValue(false),
      } as never);
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      const context = makeMockContext(extensionPath, { dismissed: true });
      await new GrokSetupService(context as never).showSetupPrompt();

      expect(context.globalState.update).toHaveBeenCalledWith(
        "nightgauge.grokSetup.dismissed",
        false
      );
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("not installed for Grok"),
        expect.objectContaining({ modal: true }),
        "Install",
        "Later",
        "Don't Show Again"
      );
    });

    it("offers an update when the skills are installed", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);
      const dir = path.join(grokHome, "skills", "nightgauge-issue-create");
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, "SKILL.md"), "# x\n");

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(makeMockContext(extensionPath) as never).showSetupPrompt();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("Update the Nightgauge Grok skills"),
        expect.objectContaining({ modal: true }),
        "Update",
        "Later",
        "Don't Show Again"
      );
    });

    it("says the CLI is missing instead of prompting", async () => {
      const vscode = await import("vscode");
      mockExecAsync.mockRejectedValue(new Error("command not found: grok"));

      const { GrokSetupService } = await import("../../src/services/GrokSetupService");
      await new GrokSetupService(makeMockContext(extensionPath) as never).showSetupPrompt();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("`grok` CLI was not found")
      );
    });
  });
});
