/**
 * OpenCodeSetupService - install Nightgauge into the operator's OpenCode (#1671)
 *
 * Writes the same tree as `scripts/install-agent-skills.sh --opencode-only
 * --yes` (and, with the opt-in plugin, `--with-plugin`) into
 * `${XDG_CONFIG_HOME:-~/.config}/opencode`:
 *
 *   skills/nightgauge-<name>/   one per bundled skill, `name:` matching the folder
 *   skills/_shared/             the skills' shared includes
 *   commands/nightgauge-<name>.md
 *   plugins/nightgauge.js and plugins/nightgauge/   only when the operator opts in
 *
 * Constraints (ADR-022, amendment 2026-09-24):
 *   - skills come only from the VSIX bundle, never the open folder's `skills/`;
 *   - nothing is written before the modal `Install` choice;
 *   - no shell, network or package-manager command: availability is probed
 *     with execFile("opencode", ["--version"]) and everything else is fs;
 *   - only `nightgauge-*` entries, `skills/_shared/` and the plugin's own
 *     files are written or pruned; opencode.json* is never touched;
 *   - any symlink at a path this service writes through is refused;
 *   - the plugin is opt-in: with it present, OpenCode npm-installs
 *     @opencode-ai/plugin into the config root on its next start.
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { execFile } from "child_process";
import { getPrefixedMainChannel } from "../utils/logger";
import { resolveBundledSkillsDir } from "./bundledSkills";

const NAMESPACE = "nightgauge-";

export function openCodeRoot(): string {
  const xdg = process.env.XDG_CONFIG_HOME;
  const base = xdg && xdg.length > 0 ? xdg : path.join(os.homedir(), ".config");
  return path.join(base, "opencode");
}

/**
 * The first frontmatter block's `description`, folded onto one line; empty
 * when there is none. A port of install-agent-skills.sh's
 * opencode_skill_description.
 */
export function skillDescription(text: string): string {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  if (lines.length === 0 || lines[0].trim() !== "---") {
    return "";
  }
  const out: string[] = [];
  let grab = false;
  for (const line of lines.slice(1)) {
    if (line.trim() === "---") {
      break;
    }
    if (grab) {
      const first = line.slice(0, 1);
      if (first === " " || first === "\t" || !line.trim()) {
        out.push(line.trim());
        continue;
      }
      break;
    }
    const m = /^description:\s*(.*)$/.exec(line);
    if (m) {
      grab = true;
      const value = m[1].trim();
      if (![">", "|", ">-", "|-", ">+", "|+"].includes(value)) {
        out.push(value);
      }
    }
  }
  let result = out
    .filter((p) => p)
    .join(" ")
    .trim();
  if (
    result.length >= 2 &&
    result[0] === result[result.length - 1] &&
    (result[0] === '"' || result[0] === "'")
  ) {
    result = result.slice(1, -1);
  }
  return result;
}

/** The `/nightgauge-<name>` command file body, byte-identical to the script's. */
export function commandBody(skill: string, description: string): string {
  return (
    "---\n" +
    `description: ${JSON.stringify(description)}\n` +
    "---\n\n" +
    `Load the \`${skill}\` skill with the skill tool and follow it.\n\n` +
    "Arguments: $ARGUMENTS\n"
  );
}

function shortName(name: string): string {
  return name.startsWith(NAMESPACE) ? name : `${NAMESPACE}${name}`;
}

function isSymlink(p: string): boolean {
  try {
    return fs.lstatSync(p).isSymbolicLink();
  } catch {
    return false;
  }
}

/** Copy a directory tree of regular files; symlinks in the source are skipped. */
function copyTree(src: string, dest: string): void {
  fs.mkdirSync(dest, { recursive: true });
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const s = path.join(src, entry.name);
    const d = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      copyTree(s, d);
    } else if (entry.isFile()) {
      fs.copyFileSync(s, d);
    }
  }
}

/** Replace dest with a copy of src (rsync -a --delete semantics for our trees). */
function syncTree(src: string, dest: string): void {
  fs.rmSync(dest, { recursive: true, force: true });
  copyTree(src, dest);
}

export interface OpenCodeInstallOptions {
  /** Bundled skills directory (the VSIX copy). */
  skillsSrc: string;
  /** Destination root, normally openCodeRoot(). */
  root: string;
  /** Bundled plugin directory holding nightgauge.js and nightgauge/; only with the opt-in. */
  pluginSrc?: string;
}

/**
 * Install skills, commands and optionally the plugin under root. Validates
 * everything before the first write; throws with an operator-facing message.
 */
export function installOpenCodeTree(opts: OpenCodeInstallOptions): {
  skills: number;
  plugin: boolean;
} {
  const { skillsSrc, root, pluginSrc } = opts;
  if (
    pluginSrc &&
    (!fs.existsSync(path.join(pluginSrc, "nightgauge.js")) ||
      !fs.existsSync(path.join(pluginSrc, "nightgauge")))
  ) {
    throw new Error(`OpenCode plugin source ${pluginSrc} is missing.`);
  }

  const names: string[] = [];
  const descriptions = new Map<string, string>();
  const missing: string[] = [];
  for (const entry of fs.readdirSync(skillsSrc, { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue;
    }
    const skillMd = path.join(skillsSrc, entry.name, "SKILL.md");
    if (!fs.existsSync(skillMd)) {
      continue;
    }
    const desc = skillDescription(fs.readFileSync(skillMd, "utf8"));
    if (!desc) {
      missing.push(entry.name);
    }
    names.push(entry.name);
    descriptions.set(entry.name, desc);
  }
  names.sort();
  if (missing.length > 0) {
    throw new Error(
      `${missing.join(", ")}: SKILL.md has no description frontmatter; OpenCode would drop it.`
    );
  }

  const skillsDir = path.join(root, "skills");
  const cmdDir = path.join(root, "commands");
  const pluginsDir = path.join(root, "plugins");
  const guarded = [
    skillsDir,
    cmdDir,
    pluginsDir,
    path.join(skillsDir, "_shared"),
    path.join(pluginsDir, "nightgauge.js"),
    path.join(pluginsDir, "nightgauge"),
  ];
  for (const name of names) {
    const short = shortName(name);
    guarded.push(path.join(skillsDir, short), path.join(cmdDir, `${short}.md`));
  }
  for (const p of guarded) {
    if (isSymlink(p)) {
      throw new Error(
        `refusing ${p}: it is a symlink; Nightgauge writes under ${root} only through real paths.`
      );
    }
  }

  fs.mkdirSync(skillsDir, { recursive: true });
  fs.mkdirSync(cmdDir, { recursive: true });
  for (const name of names) {
    const short = shortName(name);
    const dest = path.join(skillsDir, short);
    syncTree(path.join(skillsSrc, name), dest);
    // OpenCode wants the skill `name` to match its folder.
    if (short !== name) {
      const skillMd = path.join(dest, "SKILL.md");
      const text = fs.readFileSync(skillMd, "utf8").replace(/\r\n?/g, "\n");
      fs.writeFileSync(
        skillMd,
        text.replace(/^name:.*$/m, () => `name: ${short}`)
      );
    }
    fs.writeFileSync(
      path.join(cmdDir, `${short}.md`),
      commandBody(short, descriptions.get(name) ?? "")
    );
  }
  const sharedSrc = path.join(skillsSrc, "_shared");
  if (fs.existsSync(sharedSrc) && fs.statSync(sharedSrc).isDirectory()) {
    syncTree(sharedSrc, path.join(skillsDir, "_shared"));
  }

  // Prune Nightgauge entries whose skill is no longer bundled.
  const keep = new Set(names.map(shortName));
  for (const entry of fs.readdirSync(skillsDir)) {
    if (entry.startsWith(NAMESPACE) && !keep.has(entry)) {
      fs.rmSync(path.join(skillsDir, entry), { recursive: true, force: true });
    }
  }
  for (const entry of fs.readdirSync(cmdDir)) {
    if (entry.startsWith(NAMESPACE) && entry.endsWith(".md") && !keep.has(entry.slice(0, -3))) {
      fs.rmSync(path.join(cmdDir, entry), { force: true });
    }
  }

  if (pluginSrc) {
    fs.mkdirSync(pluginsDir, { recursive: true });
    fs.copyFileSync(path.join(pluginSrc, "nightgauge.js"), path.join(pluginsDir, "nightgauge.js"));
    syncTree(path.join(pluginSrc, "nightgauge"), path.join(pluginsDir, "nightgauge"));
  }
  return { skills: names.length, plugin: Boolean(pluginSrc) };
}

export class OpenCodeSetupService implements vscode.Disposable {
  private static readonly DISMISSED_KEY = "nightgauge.opencodeSetup.dismissed";
  private static readonly INSTALLED_KEY = "nightgauge.opencodeSetup.installed";

  private readonly context: vscode.ExtensionContext;
  private readonly outputChannel: vscode.OutputChannel;

  constructor(context: vscode.ExtensionContext) {
    this.context = context;
    this.outputChannel = getPrefixedMainChannel("opencode-setup");
  }

  /** Activation: prompt once, only when opencode is on PATH and nothing is installed. */
  async checkAndPromptSetup(): Promise<void> {
    const autoPrompt = vscode.workspace
      .getConfiguration("nightgauge")
      .get<boolean>("plugins.autoPrompt", true);
    if (!autoPrompt) {
      return;
    }
    if (this.context.globalState.get<boolean>(OpenCodeSetupService.DISMISSED_KEY, false)) {
      return;
    }
    if (!(await this.openCodeAvailable())) {
      return;
    }
    if (this.skillsInstalled()) {
      await this.context.globalState.update(OpenCodeSetupService.INSTALLED_KEY, true);
      return;
    }
    await this.prompt("Nightgauge skills are not installed for OpenCode. Install them now?");
  }

  /** The `nightgauge.setupOpenCode` command: always asks, installs or updates. */
  async showSetupPrompt(): Promise<void> {
    await this.context.globalState.update(OpenCodeSetupService.DISMISSED_KEY, false);
    if (!(await this.openCodeAvailable())) {
      vscode.window.showInformationMessage(
        "The `opencode` CLI was not found on PATH. Install OpenCode, then run " +
          "'Nightgauge: Setup OpenCode Skills' again. Nothing was written."
      );
      return;
    }
    await this.prompt("Install Nightgauge skills and commands into OpenCode?");
  }

  private async prompt(message: string): Promise<void> {
    const root = openCodeRoot();
    const selection = await vscode.window.showInformationMessage(
      message,
      {
        modal: true,
        detail:
          `Writes only nightgauge-* skills and commands, and skills/_shared/, under ${root}. ` +
          "opencode.json* is not edited and nothing is downloaded. " +
          "'Install with Plugin' also copies the Nightgauge plugin into plugins/; " +
          "OpenCode then npm-installs @opencode-ai/plugin there on its next start " +
          "and blocks when offline.",
      },
      "Install",
      "Install with Plugin",
      "Later",
      "Don't Show Again"
    );
    if (selection === "Install" || selection === "Install with Plugin") {
      await this.installAssets(selection === "Install with Plugin");
    } else if (selection === "Don't Show Again") {
      await this.context.globalState.update(OpenCodeSetupService.DISMISSED_KEY, true);
    }
  }

  private skillsInstalled(): boolean {
    return fs.existsSync(
      path.join(openCodeRoot(), "skills", "nightgauge-issue-create", "SKILL.md")
    );
  }

  /** execFile, no shell: the only process this service ever starts. */
  private openCodeAvailable(): Promise<boolean> {
    return new Promise((resolve) => {
      try {
        execFile("opencode", ["--version"], (error) => resolve(!error));
      } catch {
        resolve(false);
      }
    });
  }

  private async installAssets(withPlugin: boolean): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting OpenCode skill installation...\n");
    try {
      const skillsSrc = resolveBundledSkillsDir(this.context.extensionPath);
      if (!skillsSrc) {
        throw new Error(
          "No bundled skills found (dist/claude-plugins/nightgauge/skills or dist/skills)."
        );
      }
      const root = openCodeRoot();
      const pluginSrc = withPlugin
        ? path.join(this.context.extensionPath, "dist", "opencode-plugin")
        : undefined;
      const result = installOpenCodeTree({ skillsSrc, root, pluginSrc });
      await this.context.globalState.update(OpenCodeSetupService.INSTALLED_KEY, true);
      const what = `${result.skills} skills and ${result.skills} commands${
        result.plugin ? " and the plugin" : ""
      }`;
      this.outputChannel.appendLine(`✓ Synced ${what} to ${root}`);
      vscode.window.showInformationMessage(
        `Nightgauge OpenCode skills installed (${what}). Try /nightgauge-issue-create.`
      );
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`✗ Installation failed: ${errorMessage}`);
      vscode.window.showErrorMessage(`Failed to install OpenCode skills: ${errorMessage}`);
    }
  }

  dispose(): void {
    this.outputChannel.dispose();
  }
}
