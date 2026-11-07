/**
 * GrokSetupService - Grok Build TUI plugin/skill installation management
 *
 * Installs the Nightgauge plugin via `grok plugin` when possible, and always
 * copies bundled skills into ~/.grok/skills so Grok works even if plugin
 * install is flaky. The skills are the VSIX's canonical tree (dist/skills),
 * the one `scripts/install-agent-skills.sh` installs (#2762). The workspace's
 * `skills/` is used only by the separate development command,
 * installSkillsFromWorkspace().
 *
 * An upgrade re-syncs installed skills on activation, whatever
 * `plugins.autoPrompt` says: that setting governs install offers only.
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
import {
  copySkillsTree,
  installWorkspaceSkills,
  reportSkillCopy,
  resolveBundledSkillsDir,
  stampInstalledVersion,
  staleInstalledVersion,
} from "./bundledSkills";
import { ensureStableMarketplace } from "./stableMarketplace";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

const TARGET = "grok";

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

  /**
   * Activation: re-sync installed skills a different extension version
   * installed, then offer an install when `plugins.autoPrompt` allows it.
   */
  async checkAndPromptSetup(): Promise<void> {
    const staleVersion = staleInstalledVersion(this.context, TARGET, this.skillsInstalled());
    if (staleVersion) {
      this.outputChannel.appendLine(`Re-syncing Grok skills to extension ${staleVersion}...`);
      await this.resyncSkills(staleVersion);
      return;
    }

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

    await this.prompt(false);
  }

  /**
   * The `nightgauge.setupGrok` command: always offers to install or update,
   * whatever `plugins.autoPrompt` or an earlier "Don't Show Again" says.
   */
  async showSetupPrompt(): Promise<void> {
    await this.context.globalState.update(GrokSetupService.DISMISSED_KEY, false);
    const status = await this.getStatus();
    if (!status.grokCliAvailable) {
      vscode.window.showInformationMessage(
        "The `grok` CLI was not found on PATH. Install Grok, then run " +
          "'Nightgauge: Setup Grok Skills' again. Nothing was written."
      );
      return;
    }
    await this.prompt(status.nightgaugeInstalled);
  }

  private async prompt(installed: boolean): Promise<void> {
    const action = installed ? "Update" : "Install";
    const selection = await vscode.window.showInformationMessage(
      installed
        ? "Update the Nightgauge Grok skills to this extension's version?"
        : "Nightgauge skills are not installed for Grok. Install them now?",
      {
        modal: true,
        detail:
          `Copies the skills bundled with this extension into ${path.join(grokHome(), "skills")} ` +
          "and installs the Nightgauge Grok plugin. Only directories Nightgauge installed " +
          "are replaced.",
      },
      action,
      "Later",
      "Don't Show Again"
    );

    if (selection === action) {
      await this.installAssets();
    } else if (selection === "Don't Show Again") {
      await this.context.globalState.update(GrokSetupService.DISMISSED_KEY, true);
    }
  }

  private skillsInstalled(): boolean {
    return fs.existsSync(path.join(grokHome(), "skills", "nightgauge-issue-create", "SKILL.md"));
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

  private async installPluginBestEffort(): Promise<void> {
    const bundledPath = ensureStableMarketplace(this.context, (message) =>
      this.outputChannel.appendLine(`WARNING: ${message}`)
    )?.path;
    // grok's handling of a `#ref` suffix is unverified, so hand it the bare URL.
    const marketplaceSource = bundledPath ?? getMarketplaceUrl().split("#")[0];
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

  /**
   * The default install reads the VSIX bundle only, never the workspace.
   * Returns true when no skill was skipped.
   */
  private async copySkills(): Promise<boolean> {
    const dest = path.join(grokHome(), "skills");
    const source = resolveBundledSkillsDir(this.context.extensionPath);
    if (!source) {
      throw new Error("No bundled Grok skills found (dist/skills).");
    }
    const result = await copySkillsTree(
      source,
      dest,
      (message) => this.outputChannel.appendLine(`WARNING: ${message}`),
      { prune: true }
    );
    const complete = reportSkillCopy("Grok", result, this.outputChannel);
    await stampInstalledVersion(this.context, TARGET);
    return complete;
  }

  /**
   * Activation re-sync of installed skills, without opening the output
   * channel. The Grok plugin is not reinstalled: its marketplace is the stable
   * copy ensureStableMarketplace() keeps at this extension's version.
   */
  private async resyncSkills(version: string): Promise<void> {
    try {
      if (await this.copySkills()) {
        vscode.window.showInformationMessage(`Nightgauge updated its Grok skills to ${version}.`);
      }
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`✗ Re-sync failed: ${errorMessage}`);
      vscode.window.showErrorMessage(`Failed to update Grok skills: ${errorMessage}`);
    }
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
      const complete = await this.copySkills();
      await this.context.globalState.update(GrokSetupService.INSTALLED_KEY, true);
      if (complete) {
        this.outputChannel.appendLine("✓ Nightgauge Grok skills installed");
        vscode.window.showInformationMessage(
          "Nightgauge Grok skills installed. Try /nightgauge-issue-create or /issue-create."
        );
      }
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
