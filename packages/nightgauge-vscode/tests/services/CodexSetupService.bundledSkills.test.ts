/**
 * CodexSetupService copies bundled VSIX skills when the workspace has no
 * skills/ directory — the marketplace-user path.
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
}));

vi.mock("util", () => ({
  promisify: vi.fn(() => mockExecAsync),
}));

const REQUIRED_COMMANDS = [
  "nightgauge-issue-pickup.md",
  "nightgauge-feature-planning.md",
  "nightgauge-feature-dev.md",
  "nightgauge-feature-validate.md",
  "nightgauge-pr-create.md",
  "nightgauge-pr-merge.md",
];

describe("CodexSetupService bundled skills", () => {
  let tmpRoot: string;
  let extensionPath: string;
  let workspaceRoot: string;
  let codexHome: string;
  let prevCodexHome: string | undefined;

  beforeEach(async () => {
    vi.clearAllMocks();
    resetMainChannelForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-codex-bundled-"));
    extensionPath = path.join(tmpRoot, "extension");
    workspaceRoot = path.join(tmpRoot, "workspace");
    codexHome = path.join(tmpRoot, "codex-home");
    fs.mkdirSync(workspaceRoot, { recursive: true });
    prevCodexHome = process.env.CODEX_HOME;
    process.env.CODEX_HOME = codexHome;

    const commandsDir = path.join(extensionPath, "resources", "codex", "commands");
    fs.mkdirSync(commandsDir, { recursive: true });
    for (const file of REQUIRED_COMMANDS) {
      fs.writeFileSync(path.join(commandsDir, file), `# ${file}\n`);
    }

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
      { recursive: true }
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

    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn().mockReturnValue(true),
    } as never);
    (
      vscode.workspace as { workspaceFolders: typeof vscode.workspace.workspaceFolders }
    ).workspaceFolders = [{ uri: { fsPath: workspaceRoot } }] as never;
    mockExecAsync.mockResolvedValue({ stdout: "codex 1.0.0", stderr: "" });
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
      "Install Codex Commands" as never
    );
  });

  afterEach(() => {
    if (prevCodexHome === undefined) {
      delete process.env.CODEX_HOME;
    } else {
      process.env.CODEX_HOME = prevCodexHome;
    }
    fs.rmSync(tmpRoot, { recursive: true, force: true });
    vi.restoreAllMocks();
  });

  it("no workspace skills dir, bundled skills exist → dest populated", async () => {
    expect(fs.existsSync(path.join(workspaceRoot, "skills"))).toBe(false);

    const { CodexSetupService } = await import("../../src/services/CodexSetupService");
    const mockContext = {
      globalState: {
        get: vi.fn().mockReturnValue(false),
        update: vi.fn().mockResolvedValue(undefined),
      },
      extensionPath,
    };
    const service = new CodexSetupService(mockContext as never);
    await service.checkAndPromptSetup();

    const dest = path.join(codexHome, "skills", "nightgauge-issue-create", "SKILL.md");
    expect(fs.existsSync(dest)).toBe(true);
    expect(
      fs.existsSync(
        path.join(
          codexHome,
          "skills",
          "nightgauge-issue-create",
          "_includes",
          "environment-and-content.md"
        )
      )
    ).toBe(true);
    expect(fs.existsSync(path.join(codexHome, "skills", "_shared", "PREFLIGHT.md"))).toBe(true);
  });
});
