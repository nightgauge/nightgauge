/**
 * Unit tests for OpenCodeSetupService.ts (#1671)
 *
 * Consent before any write, bundle-only skill source, execFile-only probe,
 * non-Nightgauge files untouched, symlink refusal, and byte parity with
 * `scripts/install-agent-skills.sh --opencode-only --yes [--with-plugin]`.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as crypto from "crypto";
import { spawnSync } from "child_process";
import { resetMainChannelForTests } from "../../src/utils/logger";

const cp = vi.hoisted(() => ({
  exec: vi.fn(),
  execFile: vi.fn(),
  opencodePresent: true,
}));

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

// Pass-through fs whose write entry points are spies, so "wrote nothing" is observable.
vi.mock("fs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("fs")>();
  const spied = {
    ...actual,
    mkdirSync: vi.fn(actual.mkdirSync),
    writeFileSync: vi.fn(actual.writeFileSync),
    copyFileSync: vi.fn(actual.copyFileSync),
    rmSync: vi.fn(actual.rmSync),
  };
  return { ...spied, default: spied };
});

vi.mock("child_process", async (importOriginal) => {
  const actual = await importOriginal<typeof import("child_process")>();
  return { ...actual, exec: cp.exec, execFile: cp.execFile };
});

import * as vscode from "vscode";
import {
  OpenCodeSetupService,
  commandBody,
  skillDescription,
} from "../../src/services/OpenCodeSetupService";

const REPO_ROOT = path.resolve(__dirname, "..", "..", "..", "..");
const INSTALL_SCRIPT = path.join(REPO_ROOT, "scripts", "install-agent-skills.sh");
const PLUGIN_SRC = path.join(REPO_ROOT, "internal", "execution", "opencodeplugin", "plugin");

function makeContext(extensionPath: string, state: Record<string, unknown> = {}) {
  return {
    globalState: {
      get: vi.fn((key: string, def?: unknown) => (key in state ? state[key] : def)),
      update: vi.fn(async (key: string, value: unknown) => {
        state[key] = value;
      }),
    },
    extensionPath,
  } as unknown as vscode.ExtensionContext;
}

function write(file: string, text: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, text);
}

/** Fixture skills: a prefixed one, an unprefixed one (renamed), a folded description and _shared. */
function writeFixtureSkills(dir: string): void {
  write(
    path.join(dir, "nightgauge-issue-create", "SKILL.md"),
    '---\nname: nightgauge-issue-create\ndescription: Create an issue with "quotes" and ünïcode.\n---\n\nBody\n'
  );
  write(path.join(dir, "nightgauge-issue-create", "_includes", "x.md"), "include\n");
  write(
    path.join(dir, "smart-setup", "SKILL.md"),
    "---\nname: smart-setup\ndescription: >-\n  Folded description\n  over two lines\nallowed-tools: Read\n---\n\nSetup body\n"
  );
  write(
    path.join(dir, "careful", "SKILL.md"),
    "---\nname: careful\ndescription: 'Quoted gate: careful'\n---\nx\n"
  );
  write(path.join(dir, "_shared", "common.md"), "shared\n");
  write(path.join(dir, "not-a-skill", "README.md"), "no SKILL.md here\n");
}

function listTree(root: string): Map<string, string> {
  const out = new Map<string, string>();
  const walk = (dir: string) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else out.set(path.relative(root, p), fs.readFileSync(p, "utf8"));
    }
  };
  walk(root);
  return out;
}

function sha(file: string): string {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

describe("OpenCodeSetupService", () => {
  let tmp: string;
  let extensionPath: string;
  let xdg: string;
  let root: string;
  let prevXdg: string | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    resetMainChannelForTests();
    cp.opencodePresent = true;
    cp.execFile.mockImplementation(
      (_file: string, _args: string[], cb: (err: Error | null, out?: string) => void) => {
        cb(cp.opencodePresent ? null : new Error("ENOENT"), "1.18.30");
      }
    );
    tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-opencode-setup-"));
    extensionPath = path.join(tmp, "ext");
    writeFixtureSkills(path.join(extensionPath, "dist", "claude-plugins", "nightgauge", "skills"));
    fs.cpSync(PLUGIN_SRC, path.join(extensionPath, "dist", "opencode-plugin"), { recursive: true });
    xdg = path.join(tmp, "xdg");
    root = path.join(xdg, "opencode");
    prevXdg = process.env.XDG_CONFIG_HOME;
    process.env.XDG_CONFIG_HOME = xdg;
  });

  afterEach(() => {
    if (prevXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = prevXdg;
    fs.rmSync(tmp, { recursive: true, force: true });
  });

  it("Later writes nothing", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Later" as never);
    const svc = new OpenCodeSetupService(makeContext(extensionPath));
    for (const fn of [fs.mkdirSync, fs.writeFileSync, fs.copyFileSync, fs.rmSync]) {
      vi.mocked(fn).mockClear();
    }
    await svc.showSetupPrompt();
    for (const fn of [fs.mkdirSync, fs.writeFileSync, fs.copyFileSync, fs.rmSync]) {
      expect(fn).toHaveBeenCalledTimes(0);
    }
    expect(fs.existsSync(root)).toBe(false);
  });

  it("asks with a modal before writing, and Install lands files under XDG_CONFIG_HOME", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Install" as never);
    await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
    const [, opts, ...buttons] = vi.mocked(vscode.window.showInformationMessage).mock.calls[0];
    expect(opts).toMatchObject({ modal: true });
    expect(buttons).toEqual(["Install", "Install with Plugin", "Later", "Don't Show Again"]);
    expect(fs.existsSync(path.join(root, "skills", "nightgauge-issue-create", "SKILL.md"))).toBe(
      true
    );
    expect(fs.existsSync(path.join(root, "commands", "nightgauge-smart-setup.md"))).toBe(true);
    expect(fs.existsSync(path.join(root, "plugins"))).toBe(false);
  });

  it("Don't Show Again sets the dismissed key and suppresses the activation prompt", async () => {
    const state: Record<string, unknown> = {};
    const svc = new OpenCodeSetupService(makeContext(extensionPath, state));
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(
      "Don't Show Again" as never
    );
    await svc.checkAndPromptSetup();
    expect(state["nightgauge.opencodeSetup.dismissed"]).toBe(true);
    vi.mocked(vscode.window.showInformationMessage).mockClear();
    await svc.checkAndPromptSetup();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    expect(fs.existsSync(root)).toBe(false);
  });

  it("never reads skills from the open folder", async () => {
    const workspace = path.join(tmp, "workspace");
    write(path.join(workspace, "skills", "evil", "SKILL.md"), "---\ndescription: evil\n---\n");
    (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [
      { uri: { fsPath: workspace } },
    ];
    const prevCwd = process.cwd();
    process.chdir(workspace);
    try {
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Install" as never);
      await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
    } finally {
      process.chdir(prevCwd);
      (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [];
    }
    const files = [...listTree(root).keys()];
    expect(files.some((f) => f.includes("evil"))).toBe(false);
    expect(files.length).toBeGreaterThan(0);
  });

  it("runs no shell: exec is never called, execFile only as (opencode, [--version])", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(
      "Install with Plugin" as never
    );
    await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
    expect(cp.exec).not.toHaveBeenCalled();
    expect(cp.execFile).toHaveBeenCalled();
    for (const call of cp.execFile.mock.calls) {
      expect(call.slice(0, 2)).toEqual(["opencode", ["--version"]]);
    }
  });

  it("leaves opencode.json and non-Nightgauge skills byte-identical", async () => {
    write(path.join(root, "opencode.json"), '{"model":"x"}\n');
    write(path.join(root, "skills", "mine", "SKILL.md"), "---\ndescription: mine\n---\n");
    write(path.join(root, "commands", "mine.md"), "mine\n");
    const before = ["opencode.json", "skills/mine/SKILL.md", "commands/mine.md"].map((f) =>
      sha(path.join(root, f))
    );
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(
      "Install with Plugin" as never
    );
    await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
    const after = ["opencode.json", "skills/mine/SKILL.md", "commands/mine.md"].map((f) =>
      sha(path.join(root, f))
    );
    expect(after).toEqual(before);
    expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
  });

  it("refuses a skills symlink that points outside the root", async () => {
    const outside = path.join(tmp, "outside");
    write(path.join(outside, "keep.txt"), "keep\n");
    fs.mkdirSync(root, { recursive: true });
    fs.symlinkSync(outside, path.join(root, "skills"));
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Install" as never);
    await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(expect.stringContaining("symlink"));
    expect(fs.readdirSync(outside)).toEqual(["keep.txt"]);
    expect(fs.existsSync(path.join(root, "commands"))).toBe(false);
  });

  it("without opencode on PATH: no activation prompt; the command explains and writes nothing", async () => {
    cp.opencodePresent = false;
    const svc = new OpenCodeSetupService(makeContext(extensionPath));
    await svc.checkAndPromptSetup();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    await svc.showSetupPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    expect(vi.mocked(vscode.window.showInformationMessage).mock.calls[0][0]).toContain(
      "not found on PATH"
    );
    expect(fs.existsSync(root)).toBe(false);
  });

  it("skillDescription and commandBody match the script's forms", () => {
    expect(skillDescription("---\ndescription: >-\n  a\n  b\nx: y\n---\n")).toBe("a b");
    expect(skillDescription('---\ndescription: "q"\n---\n')).toBe("q");
    expect(skillDescription("no frontmatter")).toBe("");
    expect(commandBody("nightgauge-x", 'say "hi"')).toBe(
      '---\ndescription: "say \\"hi\\""\n---\n\nLoad the `nightgauge-x` skill with the skill tool and follow it.\n\nArguments: $ARGUMENTS\n'
    );
  });

  describe("parity with install-agent-skills.sh --opencode-only", () => {
    /** Run the real script against a fake repo whose skills/ is the same fixture. */
    function runScript(scriptRoot: string, xdgHome: string, withPlugin: boolean): void {
      const fakeRepo = path.join(tmp, withPlugin ? "repo-plugin" : "repo");
      fs.mkdirSync(path.join(fakeRepo, "scripts"), { recursive: true });
      fs.copyFileSync(INSTALL_SCRIPT, path.join(fakeRepo, "scripts", "install-agent-skills.sh"));
      fs.cpSync(path.join(REPO_ROOT, "scripts", "lib"), path.join(fakeRepo, "scripts", "lib"), {
        recursive: true,
      });
      writeFixtureSkills(path.join(fakeRepo, "skills"));
      fs.cpSync(
        PLUGIN_SRC,
        path.join(fakeRepo, "internal", "execution", "opencodeplugin", "plugin"),
        { recursive: true }
      );
      fs.mkdirSync(scriptRoot, { recursive: true });
      const args = [
        path.join(fakeRepo, "scripts", "install-agent-skills.sh"),
        "--opencode-only",
        "--yes",
      ];
      if (withPlugin) args.push("--with-plugin");
      const res = spawnSync("bash", args, {
        env: { ...process.env, XDG_CONFIG_HOME: xdgHome },
        encoding: "utf8",
      });
      expect(res.status, res.stderr + res.stdout).toBe(0);
    }

    for (const withPlugin of [false, true]) {
      it(`produces the same tree${withPlugin ? " with the plugin" : ""}`, async () => {
        const scriptXdg = path.join(tmp, withPlugin ? "script-xdg-plugin" : "script-xdg");
        runScript(path.join(scriptXdg, "opencode"), scriptXdg, withPlugin);

        vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(
          (withPlugin ? "Install with Plugin" : "Install") as never
        );
        await new OpenCodeSetupService(makeContext(extensionPath)).showSetupPrompt();
        expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();

        const ours = listTree(root);
        const theirs = listTree(path.join(scriptXdg, "opencode"));
        expect([...ours.keys()].sort()).toEqual([...theirs.keys()].sort());
        for (const [rel, text] of theirs) {
          expect(ours.get(rel), rel).toBe(text);
        }
      });
    }
  });
});
