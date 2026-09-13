/**
 * GrokSetupService - Grok Build TUI plugin/skill installation management
 *
 * Installs the Nightgauge plugin via `grok plugin` when possible, and always
 * copies bundled skills into ~/.grok/skills so Grok works even if plugin
 * install is flaky. The workspace's `skills/` is used only by the separate
 * development command, installSkillsFromWorkspace().
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { exec, execFile } from "child_process";
import { promisify } from "util";
import { ConfigBridge } from "./ConfigBridge";
import { DEFAULT_CONFIG } from "../config/schema";
import { getPrefixedMainChannel } from "../utils/logger";
import { copySkillsTree, installWorkspaceSkills, resolveBundledSkillsDir } from "./bundledSkills";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

const DEFAULT_MARKETPLACE_URL = "https://github.com/nightgauge/nightgauge.git";

function grokHome(): string {
  return process.env.GROK_HOME || path.join(os.homedir(), ".grok");
}

function getMarketplaceUrl(): string {
  const configBridge = ConfigBridge.getInstance();
  if (!configBridge.isInitialized()) {
    return DEFAULT_CONFIG.ui?.plugins?.marketplace_url ?? DEFAULT_MARKETPLACE_URL;
  }
  return (
    configBridge.getUI()?.plugins?.marketplace_url ??
    DEFAULT_CONFIG.ui?.plugins?.marketplace_url ??
    DEFAULT_MARKETPLACE_URL
  );
}

export class GrokSetupService implements vscode.Disposable {
  private static readonly DISMISSED_KEY = "nightgauge.grokSetup.dismissed";
  private static readonly INSTALLED_KEY = "nightgauge.grokSetup.installed";

  private readonly context: vscode.ExtensionContext;
  private readonly outputChannel: vscode.OutputChannel;

  constructor(context: vscode.ExtensionContext) {
    this.context = context;
    this.outputChannel = getPrefixedMainChannel("grok-setup");
  }

  async checkAndPromptSetup(): Promise<void> {
    const autoPrompt = vscode.workspace
      .getConfiguration("nightgauge")
      .get<boolean>("plugins.autoPrompt", true);
    if (!autoPrompt) {
      return;
    }

    const dismissed = this.context.globalState.get<boolean>(GrokSetupService.DISMISSED_KEY, false);
    if (dismissed) {
      return;
    }

    const status = await this.getStatus();
    if (!status.grokCliAvailable) {
      return;
    }

    if (status.nightgaugeInstalled) {
      await this.context.globalState.update(GrokSetupService.INSTALLED_KEY, true);
      return;
    }

    const selection = await vscode.window.showInformationMessage(
      "Nightgauge skills are not installed for Grok. Install them now?",
      { modal: true },
      "Install",
      "Later",
      "Don't Show Again"
    );

    if (selection === "Install") {
      await this.installAssets();
    } else if (selection === "Don't Show Again") {
      await this.context.globalState.update(GrokSetupService.DISMISSED_KEY, true);
    }
  }

  async showSetupPrompt(): Promise<void> {
    await this.context.globalState.update(GrokSetupService.DISMISSED_KEY, false);
    await this.checkAndPromptSetup();
  }

  private skillsInstalled(): boolean {
    const dest = path.join(grokHome(), "skills");
    return (
      fs.existsSync(path.join(dest, "nightgauge-issue-create", "SKILL.md")) ||
      fs.existsSync(path.join(dest, "issue-create", "SKILL.md"))
    );
  }

  private async getStatus(): Promise<{
    grokCliAvailable: boolean;
    nightgaugeInstalled: boolean;
  }> {
    try {
      await execAsync("grok --version");
    } catch {
      return { grokCliAvailable: false, nightgaugeInstalled: false };
    }

    if (this.skillsInstalled()) {
      return { grokCliAvailable: true, nightgaugeInstalled: true };
    }

    try {
      const { stdout } = await execAsync("grok plugin list");
      if (stdout.includes("nightgauge")) {
        return { grokCliAvailable: true, nightgaugeInstalled: true };
      }
    } catch {
      // plugin list failed — treat as not installed and fall through to prompt
    }

    return { grokCliAvailable: true, nightgaugeInstalled: false };
  }

  private getBundledMarketplacePath(): string | undefined {
    const marketplacePath = path.join(
      this.context.extensionPath,
      "dist",
      ".claude-plugin",
      "marketplace.json"
    );
    if (fs.existsSync(marketplacePath)) {
      return path.join(this.context.extensionPath, "dist");
    }
    return undefined;
  }

  private async installPluginBestEffort(): Promise<void> {
    const bundledPath = this.getBundledMarketplacePath();
    const marketplaceSource = bundledPath ?? getMarketplaceUrl();
    try {
      await execFileAsync("grok", ["plugin", "marketplace", "add", marketplaceSource]);
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`WARNING: grok plugin marketplace add failed: ${msg}`);
    }

    const pluginDir = bundledPath ? path.join(bundledPath, "claude-plugins", "nightgauge") : "";
    const installTargets =
      pluginDir && fs.existsSync(pluginDir) ? [pluginDir, "nightgauge"] : ["nightgauge"];

    for (const target of installTargets) {
      try {
        await execFileAsync("grok", ["plugin", "install", target, "--trust"]);
        this.outputChannel.appendLine(`✓ grok plugin install ${target}`);
        return;
      } catch (error) {
        const msg = error instanceof Error ? error.message : String(error);
        this.outputChannel.appendLine(`WARNING: grok plugin install ${target} failed: ${msg}`);
      }
    }
  }

  /** The default install reads the VSIX bundle only, never the workspace. */
  private async copySkills(): Promise<void> {
    const dest = path.join(grokHome(), "skills");
    const source = resolveBundledSkillsDir(this.context.extensionPath);
    if (!source) {
      throw new Error(
        "No bundled Grok skills found (dist/claude-plugins/nightgauge/skills or dist/skills)."
      );
    }
    const count = await copySkillsTree(source, dest, (message) =>
      this.outputChannel.appendLine(`WARNING: ${message}`)
    );
    this.outputChannel.appendLine(`✓ Copied ${count} skill directories to ${dest}`);
  }

  /**
   * Development only: install ~/.grok/skills from the open workspace's
   * `skills/`, behind workspace trust and a modal confirmation.
   */
  async installSkillsFromWorkspace(): Promise<void> {
    await installWorkspaceSkills("Grok", path.join(grokHome(), "skills"), this.outputChannel);
  }

  private async installAssets(): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting Grok skill installation...\n");

    try {
      await this.installPluginBestEffort();
      await this.copySkills();
      await this.context.globalState.update(GrokSetupService.INSTALLED_KEY, true);
      this.outputChannel.appendLine("✓ Nightgauge Grok skills installed");
      vscode.window.showInformationMessage(
        "Nightgauge Grok skills installed. Try /nightgauge-issue-create or /issue-create."
      );
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`✗ Installation failed: ${errorMessage}`);
      vscode.window.showErrorMessage(`Failed to install Grok skills: ${errorMessage}`);
    }
  }

  dispose(): void {
    this.outputChannel.dispose();
  }
}
