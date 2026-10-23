/**
 * GrokSetupService skill source (#1683): the default install copies the
 * extension's bundled skills into ~/.grok/skills and never the open
 * workspace's `skills/`. The workspace is a source only through the
 * development command, which refuses untrusted workspaces and a destination
 * that overlaps the source, and names the absolute source and resolved
 * destination paths in a modal confirmation.
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
  execFile: vi.fn(),
}));

vi.mock("util", () => ({
  promisify: vi.fn(() => mockExecAsync),
}));

const BUNDLED = "# issue-create (bundled)\n";
const WORKSPACE = "# issue-create (workspace)\n";

function write(file: string, content: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
}

function makeMockContext(extensionPath: string) {
  return {
    globalState: {
      get: vi.fn((_key: string, defaultValue?: unknown) => defaultValue ?? false),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath,
  };
}

describe("GrokSetupService skill source", () => {
  let tmpRoot: string;
  let extensionPath: string;
  let workspaceRoot: string;
  let grokHome: string;
  let skillsDest: string;
  let prevGrokHome: string | undefined;

  beforeEach(async () => {
    vi.clearAllMocks();
    resetMainChannelForTests();
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-grok-skill-source-"));
    extensionPath = path.join(tmpRoot, "extension");
    workspaceRoot = path.join(tmpRoot, "workspace");
    grokHome = path.join(tmpRoot, "grok-home");
    skillsDest = path.join(grokHome, "skills");
    prevGrokHome = process.env.GROK_HOME;
    process.env.GROK_HOME = grokHome;

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
    mockExecAsync.mockImplementation(async (cmd: unknown) =>
      typeof cmd === "string" && cmd.includes("--version")
        ? { stdout: "grok 1.0.0", stderr: "" }
        : { stdout: "", stderr: "" }
    );
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

  async function makeService() {
    const { GrokSetupService } = await import("../../src/services/GrokSetupService");
    return new GrokSetupService(makeMockContext(extensionPath) as never);
  }

  it("default install copies the bundle's skills and not the workspace's", async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install" as never);

    await (await makeService()).checkAndPromptSetup();

    const installed = path.join(skillsDest, "nightgauge-issue-create", "SKILL.md");
    expect(fs.readFileSync(installed, "utf8")).toBe(BUNDLED);
    expect(fs.existsSync(path.join(skillsDest, "_shared", "PREFLIGHT.md"))).toBe(true);
    expect(fs.existsSync(path.join(skillsDest, "evil"))).toBe(false);
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
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

  it("the development command names the resolved destination in its confirmation", async () => {
    const realSkills = path.join(tmpRoot, "real-grok-skills");
    fs.mkdirSync(realSkills);
    fs.mkdirSync(grokHome);
    fs.symlinkSync(realSkills, skillsDest);
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as never);

    await (await makeService()).installSkillsFromWorkspace();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        modal: true,
        detail: expect.stringContaining(fs.realpathSync(realSkills)),
      }),
      "Install from Workspace"
    );
  });

  it("the development command refuses when ~/.grok/skills is the workspace's skills/", async () => {
    const workspaceSkills = path.join(workspaceRoot, "skills");
    fs.mkdirSync(grokHome);
    fs.symlinkSync(workspaceSkills, skillsDest);
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(
      "Install from Workspace" as never
    );

    await (await makeService()).installSkillsFromWorkspace();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining(fs.realpathSync(workspaceSkills))
    );
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    const skill = path.join(workspaceSkills, "nightgauge-issue-create", "SKILL.md");
    expect(fs.readFileSync(skill, "utf8")).toBe(WORKSPACE);
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
