/**
 * CodexSetupService skill source (#1683): the default install copies the
 * extension's bundled skills and slash commands into ~/.codex, and never the
 * open workspace's `skills/` or `.codex/commands/`. The workspace's `skills/`
 * is a source only through the development command, which refuses untrusted
 * workspaces and names the absolute source path in a modal confirmation.
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
    showWarningMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(true),
    })),
    workspaceFolders: [] as { uri: { fsPath: string } }[],
    isTrusted: true,
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

const BUNDLED = "# issue-create (bundled)\n";
const WORKSPACE = "# issue-create (workspace)\n";

function write(file: string, content: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
}

function makeMockContext(extensionPath: string) {
  return {
    globalState: {
      get: vi.fn().mockReturnValue(false),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath,
  };
}

describe("CodexSetupService skill source", () => {
  let tmpRoot: string;
  let extensionPath: string;
  let workspaceRoot: string;
  let codexHome: string;
  let skillsDest: string;
  let prevCodexHome: string | undefined;

  beforeEach(async () => {
    vi.clearAllMocks();
    resetMainChannelForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-codex-skill-source-"));
    extensionPath = path.join(tmpRoot, "extension");
    workspaceRoot = path.join(tmpRoot, "workspace");
    codexHome = path.join(tmpRoot, "codex-home");
    skillsDest = path.join(codexHome, "skills");
    prevCodexHome = process.env.CODEX_HOME;
    process.env.CODEX_HOME = codexHome;

    for (const file of REQUIRED_COMMANDS) {
      write(path.join(extensionPath, "resources", "codex", "commands", file), `# ${file}\n`);
    }

    // The fake bundle: the plugin mirror layout the VSIX ships.
    const bundle = path.join(extensionPath, "dist", "claude-plugins", "nightgauge", "skills");
    write(path.join(bundle, "issue-create", "SKILL.md"), BUNDLED);
    write(path.join(bundle, "_shared", "PREFLIGHT.md"), "# preflight\n");

    // The trusted workspace: a skill of its own, and one that shadows a
    // Nightgauge skill name with different content.
    write(path.join(workspaceRoot, "skills", "evil", "SKILL.md"), "# evil\n");
    write(path.join(workspaceRoot, "skills", "nightgauge-issue-create", "SKILL.md"), WORKSPACE);

    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn().mockReturnValue(true),
    } as never);
    const workspace = vscode.workspace as { workspaceFolders: unknown; isTrusted: boolean };
    workspace.workspaceFolders = [{ uri: { fsPath: workspaceRoot } }];
    workspace.isTrusted = true;
    mockExecAsync.mockResolvedValue({ stdout: "codex 1.0.0", stderr: "" });
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

  async function makeService() {
    const { CodexSetupService } = await import("../../src/services/CodexSetupService");
    return new CodexSetupService(makeMockContext(extensionPath) as never);
  }

  it("default install copies the bundle's skills and not the workspace's", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
      "Install Codex Commands" as never
    );

    await (await makeService()).checkAndPromptSetup();

    const installed = path.join(skillsDest, "nightgauge-issue-create", "SKILL.md");
    expect(fs.readFileSync(installed, "utf8")).toBe(BUNDLED);
    expect(fs.existsSync(path.join(skillsDest, "_shared", "PREFLIGHT.md"))).toBe(true);
    expect(fs.existsSync(path.join(skillsDest, "evil"))).toBe(false);
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
  });

  it("default install copies the bundled Codex commands, never the workspace's", async () => {
    // Every Codex session loads ~/.codex/commands, so a workspace's
    // .codex/commands must not reach it: neither its text nor a link it holds.
    const secret = path.join(tmpRoot, "secret.txt");
    write(secret, "# a local secret\n");
    for (const file of REQUIRED_COMMANDS) {
      write(path.join(workspaceRoot, ".codex", "commands", file), "# FROM WORKSPACE\n");
    }
    const linked = path.join(workspaceRoot, ".codex", "commands", "nightgauge-pr-merge.md");
    fs.rmSync(linked);
    fs.symlinkSync(secret, linked);
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
      "Install Codex Commands" as never
    );

    await (await makeService()).checkAndPromptSetup();

    for (const file of REQUIRED_COMMANDS) {
      expect(fs.readFileSync(path.join(codexHome, "commands", file), "utf8")).toBe(`# ${file}\n`);
    }
  });

  it("the development command refuses an untrusted workspace", async () => {
    const vscode = await import("vscode");
    (vscode.workspace as { isTrusted: boolean }).isTrusted = false;

    await (await makeService()).installSkillsFromWorkspace();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("untrusted workspace")
    );
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    expect(fs.existsSync(skillsDest)).toBe(false);
  });

  it("the development command names the absolute source and copies nothing unconfirmed", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as never);

    await (await makeService()).installSkillsFromWorkspace();

    const source = fs.realpathSync(path.join(workspaceRoot, "skills"));
    expect(path.isAbsolute(source)).toBe(true);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining(source),
      expect.objectContaining({ modal: true }),
      "Install from Workspace"
    );
    expect(fs.existsSync(skillsDest)).toBe(false);
  });

  it("the development command installs the workspace's Nightgauge skills once confirmed", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(
      "Install from Workspace" as never
    );

    await (await makeService()).installSkillsFromWorkspace();

    const installed = path.join(skillsDest, "nightgauge-issue-create", "SKILL.md");
    expect(fs.readFileSync(installed, "utf8")).toBe(WORKSPACE);
    // `evil` is not a Nightgauge skill name, so even this path never writes it.
    expect(fs.existsSync(path.join(skillsDest, "evil"))).toBe(false);
  });
});
