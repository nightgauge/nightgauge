/**
 * CodexSetupService - Codex CLI skill installation management
 *
 * Copies the VSIX's canonical skills (dist/skills) into ~/.codex/skills, which
 * Codex loads and exposes as `$<skill-name>`: the tree
 * `scripts/install-agent-skills.sh` installs. Codex does not read
 * ~/.codex/commands, so nothing is installed there (#2762). A workspace's
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
import { exec } from "child_process";
import { promisify } from "util";
import { getPrefixedMainChannel } from "../utils/logger";
import {
  copySkillsTree,
  installWorkspaceSkills,
  reportSkillCopy,
  resolveBundledSkillsDir,
  stampInstalledVersion,
  staleInstalledVersion,
} from "./bundledSkills";

const execAsync = promisify(exec);

const TARGET = "codex";
const ENTRY_SKILL = "nightgauge-issue-pickup";

function codexHome(): string {
  return process.env.CODEX_HOME || path.join(os.homedir(), ".codex");
}

function codexSkillsDir(): string {
  return path.join(codexHome(), "skills");
}

export class CodexSetupService implements vscode.Disposable {
  private static readonly DISMISSED_KEY = "nightgauge.codexSetup.dismissed";
  private static readonly INSTALLED_KEY = "nightgauge.codexSetup.installed";

  private readonly context: vscode.ExtensionContext;
  private readonly outputChannel: vscode.OutputChannel;

  constructor(context: vscode.ExtensionContext) {
    this.context = context;
    // Folded into the shared main channel behind a "codex-setup" tag rather
    // than its own destination — retired six-channel split (#749).
    this.outputChannel = getPrefixedMainChannel("codex-setup");
  }

  /**
   * Activation: re-sync installed skills a different extension version
   * installed, then offer an install when `plugins.autoPrompt` allows it.
   */
  async checkAndPromptSetup(): Promise<void> {
    const staleVersion = staleInstalledVersion(this.context, TARGET, this.skillsInstalled());
    if (staleVersion) {
      this.outputChannel.appendLine(`Re-syncing Codex skills to extension ${staleVersion}...`);
      await this.installAssets(staleVersion);
      return;
    }

    const autoPrompt = vscode.workspace
      .getConfiguration("nightgauge")
      .get<boolean>("plugins.autoPrompt", true);
    if (!autoPrompt) {
      return;
    }

    const dismissed = this.context.globalState.get<boolean>(CodexSetupService.DISMISSED_KEY, false);
    if (dismissed) {
      return;
    }

    const status = await this.getStatus();

    // If Codex CLI isn't installed, skip silently.
    if (!status.codexCliAvailable) {
      return;
    }

    if (status.skillsInstalled) {
      await this.context.globalState.update(CodexSetupService.INSTALLED_KEY, true);
      return;
    }

    await this.prompt(false);
  }

  /**
   * The `nightgauge.setupCodex` command: always offers to install or update,
   * whatever `plugins.autoPrompt` or an earlier "Don't Show Again" says.
   */
  async showSetupPrompt(): Promise<void> {
    await this.context.globalState.update(CodexSetupService.DISMISSED_KEY, false);
    const status = await this.getStatus();
    if (!status.codexCliAvailable) {
      vscode.window.showInformationMessage(
        "The `codex` CLI was not found on PATH. Install Codex, then run " +
          "'Nightgauge: Setup Codex Skills' again. Nothing was written."
      );
      return;
    }
    await this.prompt(status.skillsInstalled);
  }

  private async prompt(installed: boolean): Promise<void> {
    const action = installed ? "Update Codex Skills" : "Install Codex Skills";
    const selection = await vscode.window.showInformationMessage(
      installed
        ? "Update the Nightgauge Codex skills to this extension's version?"
        : "Nightgauge Codex skills are not installed. Install them now?",
      {
        modal: true,
        detail:
          `Copies the skills bundled with this extension into ${codexSkillsDir()}. ` +
          "Only directories Nightgauge installed are replaced.",
      },
      action,
      "Later",
      "Don't Show Again"
    );

    if (selection === action) {
      await this.installAssets();
    } else if (selection === "Don't Show Again") {
      await this.context.globalState.update(CodexSetupService.DISMISSED_KEY, true);
    }
  }

  private skillsInstalled(): boolean {
    return fs.existsSync(path.join(codexSkillsDir(), ENTRY_SKILL, "SKILL.md"));
  }

  private async getStatus(): Promise<{
    codexCliAvailable: boolean;
    skillsInstalled: boolean;
  }> {
    try {
      await execAsync("codex --version");
    } catch {
      return { codexCliAvailable: false, skillsInstalled: false };
    }
    return { codexCliAvailable: true, skillsInstalled: this.skillsInstalled() };
  }

  /**
   * Install from the VSIX bundle only, never the workspace: the development
   * source is installSkillsFromWorkspace(). A re-sync to `resyncVersion` does
   * not open the output channel.
   */
  private async installAssets(resyncVersion?: string): Promise<void> {
    if (!resyncVersion) {
      this.outputChannel.show(true);
      this.outputChannel.appendLine("Starting Codex skill installation...\n");
    }

    try {
      const source = resolveBundledSkillsDir(this.context.extensionPath);
      if (!source) {
        throw new Error("No bundled Codex skills found (dist/skills).");
      }
      const dest = codexSkillsDir();
      const result = await copySkillsTree(
        source,
        dest,
        (message) => this.outputChannel.appendLine(`WARNING: ${message}`),
        { prune: true }
      );
      const complete = reportSkillCopy("Codex", result, this.outputChannel);

      await this.context.globalState.update(CodexSetupService.INSTALLED_KEY, true);
      await stampInstalledVersion(this.context, TARGET);
      this.outputChannel.appendLine(`✓ Destination: ${dest}`);

      if (complete) {
        vscode.window.showInformationMessage(
          resyncVersion
            ? `Nightgauge updated its Codex skills to ${resyncVersion}.`
            : `Nightgauge Codex skills installed. Try $${ENTRY_SKILL}.`
        );
      }
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`✗ Installation failed: ${errorMessage}`);
      vscode.window.showErrorMessage(`Failed to install Codex skills: ${errorMessage}`);
    }
  }

  /**
   * Development only: install ~/.codex/skills from the open workspace's
   * `skills/`, behind workspace trust and a modal confirmation.
   */
  async installSkillsFromWorkspace(): Promise<void> {
    await installWorkspaceSkills("Codex", codexSkillsDir(), this.outputChannel);
  }

  dispose(): void {
    this.outputChannel.dispose();
  }
}
