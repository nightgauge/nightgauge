/**
 * CodexSetupService copies bundled VSIX skills when the workspace has no
 * skills/ directory — the marketplace-user path — and copySkillsTree keeps
 * every install inside its source tree and off entries Nightgauge does not
 * own (#1683).
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

describe("copySkillsTree containment and ownership", () => {
  let tmpRoot: string;
  let source: string;
  let dest: string;

  function write(file: string, content: string): void {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content);
  }

  /** True when anything, including a dangling symlink, sits at `p`. */
  function present(p: string): boolean {
    try {
      fs.lstatSync(p);
      return true;
    } catch {
      return false;
    }
  }

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-copy-skills-"));
    source = path.join(tmpRoot, "source");
    dest = path.join(tmpRoot, "dest");
    write(path.join(source, "nightgauge-issue-create", "SKILL.md"), "# issue-create\n");
    write(path.join(source, "_shared", "PREFLIGHT.md"), "# preflight\n");
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("skips a symlink to /etc and logs a warning", async () => {
    fs.symlinkSync("/etc", path.join(source, "nightgauge-issue-create", "etc"));
    fs.symlinkSync("/etc", path.join(source, "nightgauge-etc"));
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(source, dest, warn);

    const realSource = fs.realpathSync(source);
    expect(count).toBe(2);
    expect(fs.existsSync(path.join(dest, "nightgauge-issue-create", "SKILL.md"))).toBe(true);
    expect(present(path.join(dest, "nightgauge-issue-create", "etc"))).toBe(false);
    expect(present(path.join(dest, "nightgauge-etc"))).toBe(false);
    // The containment check's own reason, not the linked-directory one.
    for (const link of [
      path.join(realSource, "nightgauge-issue-create", "etc"),
      path.join(realSource, "nightgauge-etc"),
    ]) {
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining(`${link} -> /etc: it does not resolve inside ${realSource}`)
      );
    }
  });

  it("skips a linked file that resolves outside the source tree and logs a warning", async () => {
    // A skill file linked to a local secret, one linked to a system file, and
    // a skill whose SKILL.md is such a link: none may reach the agent's
    // skills directory, which every later session loads.
    const secret = path.join(tmpRoot, "secret.txt");
    write(secret, "# a local secret\n");
    const skill = path.join(source, "nightgauge-issue-create");
    fs.symlinkSync(path.join("..", "..", "secret.txt"), path.join(skill, "notes.md"));
    fs.symlinkSync("/etc/hosts", path.join(skill, "hosts.md"));
    fs.mkdirSync(path.join(source, "nightgauge-leak"));
    fs.symlinkSync(secret, path.join(source, "nightgauge-leak", "SKILL.md"));
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(source, dest, warn);

    const realSource = fs.realpathSync(source);
    expect(count).toBe(2);
    expect(fs.readFileSync(path.join(dest, "nightgauge-issue-create", "SKILL.md"), "utf8")).toBe(
      "# issue-create\n"
    );
    expect(present(path.join(dest, "nightgauge-issue-create", "notes.md"))).toBe(false);
    expect(present(path.join(dest, "nightgauge-issue-create", "hosts.md"))).toBe(false);
    expect(present(path.join(dest, "nightgauge-leak"))).toBe(false);
    for (const [link, target] of [
      [path.join(realSource, "nightgauge-issue-create", "notes.md"), "../../secret.txt"],
      [path.join(realSource, "nightgauge-issue-create", "hosts.md"), "/etc/hosts"],
      [path.join(realSource, "nightgauge-leak", "SKILL.md"), secret],
    ]) {
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining(`${link} -> ${target}: it does not resolve inside ${realSource}`)
      );
    }
  });

  it("installs only SKILL.md directories and _shared/", async () => {
    // The plugin-mirror layout the VSIX ships: short names, installed under
    // the restored nightgauge- prefix, so a directory without a SKILL.md file
    // would land as nightgauge-<name> in every user's skills directory.
    const mirror = path.join(tmpRoot, "mirror");
    write(path.join(mirror, "issue-create", "SKILL.md"), "# issue-create\n");
    write(path.join(mirror, "_shared", "PREFLIGHT.md"), "# preflight\n");
    write(path.join(mirror, "templates", "README.md"), "# templates\n");
    fs.mkdirSync(path.join(mirror, "broken", "SKILL.md"), { recursive: true });
    write(path.join(mirror, "README.md"), "# mirror\n");

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(mirror, dest, vi.fn());

    expect(count).toBe(2);
    expect(fs.readdirSync(dest).sort()).toEqual(["_shared", "nightgauge-issue-create"]);
  });

  it("copies a symlink that stays inside the source tree as a regular file", async () => {
    fs.symlinkSync(
      path.join("..", "_shared", "PREFLIGHT.md"),
      path.join(source, "nightgauge-issue-create", "preflight.md")
    );
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    await copySkillsTree(source, dest, warn);

    const copied = path.join(dest, "nightgauge-issue-create", "preflight.md");
    expect(fs.lstatSync(copied).isFile()).toBe(true);
    expect(fs.readFileSync(copied, "utf8")).toBe("# preflight\n");
    expect(warn).not.toHaveBeenCalled();
  });

  it("skips a symlink that loops back to a directory being copied", async () => {
    fs.symlinkSync("..", path.join(source, "nightgauge-issue-create", "loop"));
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    await copySkillsTree(source, dest, warn);

    expect(present(path.join(dest, "nightgauge-issue-create", "loop"))).toBe(false);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(path.join(fs.realpathSync(source), "nightgauge-issue-create", "loop"))
    );
  });

  it("never copies a directory through a symlink, so links cannot fan a tree out", async () => {
    // Each level links to the next level twice. Following directory links
    // copies level N 2^N times over; the real tree holds one file per level.
    const levels = 8;
    const skill = path.join(source, "nightgauge-issue-create");
    for (let i = 0; i < levels; i++) {
      write(path.join(skill, `l${i}`, "file.md"), `# level ${i}\n`);
    }
    for (let i = 0; i < levels - 1; i++) {
      fs.symlinkSync(path.join("..", `l${i + 1}`), path.join(skill, `l${i}`, "a"));
      fs.symlinkSync(path.join("..", `l${i + 1}`), path.join(skill, `l${i}`, "b"));
    }
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    await copySkillsTree(source, dest, warn);

    const copied = fs
      .readdirSync(path.join(dest, "nightgauge-issue-create"), { recursive: true })
      .map(String)
      .filter((p) => p.endsWith("file.md"));
    expect(copied).toHaveLength(levels);
    expect(present(path.join(dest, "nightgauge-issue-create", "l0", "a"))).toBe(false);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(path.join(fs.realpathSync(skill), "l0", "a"))
    );
  });

  it("leaves destination entries Nightgauge does not own untouched", async () => {
    write(path.join(source, "my-skill", "SKILL.md"), "# from the source\n");
    write(path.join(dest, "my-skill", "SKILL.md"), "# the operator's own\n");
    // An operator's development symlink over a Nightgauge skill name.
    const devCheckout = path.join(tmpRoot, "dev-checkout");
    write(path.join(devCheckout, "SKILL.md"), "# the operator's checkout\n");
    fs.symlinkSync(devCheckout, path.join(dest, "nightgauge-issue-create"));
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(source, dest, warn);

    expect(count).toBe(1);
    expect(fs.readFileSync(path.join(dest, "my-skill", "SKILL.md"), "utf8")).toBe(
      "# the operator's own\n"
    );
    expect(fs.lstatSync(path.join(dest, "nightgauge-issue-create")).isSymbolicLink()).toBe(true);
    expect(fs.readFileSync(path.join(devCheckout, "SKILL.md"), "utf8")).toBe(
      "# the operator's checkout\n"
    );
    expect(fs.existsSync(path.join(dest, "_shared", "PREFLIGHT.md"))).toBe(true);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining(path.join(dest, "my-skill")));
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(path.join(dest, "nightgauge-issue-create"))
    );
  });

  it("leaves an operator's own directory at a generic Nightgauge name untouched", async () => {
    write(path.join(source, "update-docs", "SKILL.md"), "# update-docs (Nightgauge)\n");
    // The operator's own update-docs skill and another skill pack's _shared/.
    write(path.join(dest, "update-docs", "SKILL.md"), "# the operator's own\n");
    write(path.join(dest, "update-docs", "notes.md"), "# the operator's notes\n");
    write(path.join(dest, "_shared", "other-pack.md"), "# another pack\n");
    const warn = vi.fn();

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(source, dest, warn);

    expect(count).toBe(1);
    expect(fs.readFileSync(path.join(dest, "update-docs", "SKILL.md"), "utf8")).toBe(
      "# the operator's own\n"
    );
    expect(fs.existsSync(path.join(dest, "update-docs", "notes.md"))).toBe(true);
    expect(fs.existsSync(path.join(dest, "_shared", "other-pack.md"))).toBe(true);
    expect(fs.existsSync(path.join(dest, "_shared", "PREFLIGHT.md"))).toBe(false);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining(path.join(dest, "update-docs")));
    expect(warn).toHaveBeenCalledWith(expect.stringContaining(path.join(dest, "_shared")));
  });

  it("leaves a nightgauge- directory without the install marker untouched", async () => {
    // An agent's skills directory linked to a checkout's skills/: the name is
    // Nightgauge's, but the directory holds uncommitted work and no marker.
    const checkoutSkills = path.join(tmpRoot, "checkout", "skills");
    const checkoutSkill = path.join(checkoutSkills, "nightgauge-issue-create");
    write(path.join(checkoutSkill, "SKILL.md"), "# uncommitted edit\n");
    write(path.join(checkoutSkill, "_includes", "new-file.md"), "# untracked\n");
    write(path.join(checkoutSkills, "_shared", "PREFLIGHT.md"), "# checkout preflight\n");
    const agentSkills = path.join(tmpRoot, "agent-home", "skills");
    fs.mkdirSync(path.dirname(agentSkills));
    fs.symlinkSync(checkoutSkills, agentSkills);
    const warn = vi.fn();

    const { copySkillsTree, INSTALL_MARKER } = await import("../../src/services/bundledSkills");
    const count = await copySkillsTree(source, agentSkills, warn);

    expect(count).toBe(0);
    expect(fs.readFileSync(path.join(checkoutSkill, "SKILL.md"), "utf8")).toBe(
      "# uncommitted edit\n"
    );
    expect(fs.existsSync(path.join(checkoutSkill, "_includes", "new-file.md"))).toBe(true);
    expect(present(path.join(checkoutSkill, INSTALL_MARKER))).toBe(false);
    expect(present(path.join(checkoutSkills, "_shared", INSTALL_MARKER))).toBe(false);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(
        `${path.join(agentSkills, "nightgauge-issue-create")} is not a directory Nightgauge installed`
      )
    );
  });

  it("refreshes every directory it installed, recognised by its install marker", async () => {
    write(path.join(source, "update-docs", "SKILL.md"), "# update-docs v1\n");
    const { copySkillsTree, INSTALL_MARKER } = await import("../../src/services/bundledSkills");
    await copySkillsTree(source, dest, vi.fn());
    expect(fs.lstatSync(path.join(dest, "_shared", INSTALL_MARKER)).isFile()).toBe(true);
    expect(fs.lstatSync(path.join(dest, "update-docs", INSTALL_MARKER)).isFile()).toBe(true);

    write(path.join(dest, "_shared", "STALE.md"), "# stale\n");
    write(path.join(source, "update-docs", "SKILL.md"), "# update-docs v2\n");
    const warn = vi.fn();
    const count = await copySkillsTree(source, dest, warn);

    expect(count).toBe(3);
    expect(fs.existsSync(path.join(dest, "_shared", "STALE.md"))).toBe(false);
    expect(fs.existsSync(path.join(dest, "_shared", "PREFLIGHT.md"))).toBe(true);
    expect(fs.readFileSync(path.join(dest, "update-docs", "SKILL.md"), "utf8")).toBe(
      "# update-docs v2\n"
    );
    expect(warn).not.toHaveBeenCalled();
  });

  it("refuses a destination that is the source, and leaves the source intact", async () => {
    // A developer's agent skills directory symlinked to the checkout's skills/.
    const agentHome = path.join(tmpRoot, "agent-home");
    fs.mkdirSync(agentHome);
    fs.symlinkSync(source, path.join(agentHome, "skills"));

    const { copySkillsTree } = await import("../../src/services/bundledSkills");
    await expect(copySkillsTree(source, path.join(agentHome, "skills"), vi.fn())).rejects.toThrow(
      fs.realpathSync(source)
    );
    await expect(
      copySkillsTree(source, path.join(source, "nested", "dest"), vi.fn())
    ).rejects.toThrow(fs.realpathSync(source));

    expect(fs.readFileSync(path.join(source, "nightgauge-issue-create", "SKILL.md"), "utf8")).toBe(
      "# issue-create\n"
    );
    expect(fs.existsSync(path.join(source, "_shared", "PREFLIGHT.md"))).toBe(true);
    expect(present(path.join(source, "nested"))).toBe(false);
  });
});
