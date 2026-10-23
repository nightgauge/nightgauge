/**
 * CodexSetupService - Codex CLI command/skill installation management
 *
 * Installs Nightgauge Codex slash commands into ~/.codex/commands and copies
 * skills into ~/.codex/skills, both from the VSIX bundle. A workspace's
 * `.codex/commands/` is never copied, and its `skills/` is used only by the
 * separate development command, installSkillsFromWorkspace().
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { exec } from "child_process";
import { promisify } from "util";
import { getPrefixedMainChannel } from "../utils/logger";
import { copySkillsTree, installWorkspaceSkills, resolveBundledSkillsDir } from "./bundledSkills";

const execAsync = promisify(exec);

function codexHome(): string {
  return process.env.CODEX_HOME || path.join(os.homedir(), ".codex");
}

const REQUIRED_COMMANDS = [
  "nightgauge-issue-pickup.md",
  "nightgauge-feature-planning.md",
  "nightgauge-feature-dev.md",
  "nightgauge-feature-validate.md",
  "nightgauge-pr-create.md",
  "nightgauge-pr-merge.md",
] as const;

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

  async checkAndPromptSetup(): Promise<void> {
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

    if (status.commandsInstalled) {
      await this.context.globalState.update(CodexSetupService.INSTALLED_KEY, true);
      return;
    }

    const selection = await vscode.window.showInformationMessage(
      "Nightgauge Codex slash commands are not installed. Install them now?",
      { modal: true },
      "Install Codex Commands",
      "Later",
      "Don't Show Again"
    );

    if (selection === "Install Codex Commands") {
      await this.installAssets();
    } else if (selection === "Don't Show Again") {
      await this.context.globalState.update(CodexSetupService.DISMISSED_KEY, true);
    }
  }

  async showSetupPrompt(): Promise<void> {
    await this.context.globalState.update(CodexSetupService.DISMISSED_KEY, false);
    await this.checkAndPromptSetup();
  }

  private async getStatus(): Promise<{
    codexCliAvailable: boolean;
    commandsInstalled: boolean;
  }> {
    let codexCliAvailable: boolean;
    try {
      await execAsync("codex --version");
      codexCliAvailable = true;
    } catch {
      return { codexCliAvailable: false, commandsInstalled: false };
    }

    const commandsDir = path.join(codexHome(), "commands");
    const commandsInstalled = REQUIRED_COMMANDS.every((file) =>
      fs.existsSync(path.join(commandsDir, file))
    );

    return { codexCliAvailable, commandsInstalled };
  }

  /**
   * The VSIX bundle's command definitions, never a workspace's
   * `.codex/commands/`: every Codex session loads ~/.codex/commands.
   */
  private resolveCommandsSourceDir(): string | null {
    const bundledSource = path.join(this.context.extensionPath, "resources", "codex", "commands");
    return fs.existsSync(bundledSource) ? bundledSource : null;
  }

  private async installAssets(): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting Codex asset installation...\n");

    try {
      const sourceCommandsDir = this.resolveCommandsSourceDir();
      if (!sourceCommandsDir) {
        throw new Error("No bundled Codex commands found (resources/codex/commands).");
      }

      const commandsDest = path.join(codexHome(), "commands");
      await fs.promises.mkdir(commandsDest, { recursive: true });

      for (const file of REQUIRED_COMMANDS) {
        await fs.promises.copyFile(
          path.join(sourceCommandsDir, file),
          path.join(commandsDest, file)
        );
      }

      // Skill sync reads the VSIX bundle only, never the workspace: the
      // development source is installSkillsFromWorkspace().
      const sourceSkillsDir = resolveBundledSkillsDir(this.context.extensionPath);
      if (sourceSkillsDir) {
        await copySkillsTree(sourceSkillsDir, path.join(codexHome(), "skills"), (message) =>
          this.outputChannel.appendLine(`WARNING: ${message}`)
        );
      }

      await this.context.globalState.update(CodexSetupService.INSTALLED_KEY, true);

      this.outputChannel.appendLine("✓ Codex commands installed");
      this.outputChannel.appendLine(`✓ Destination: ${commandsDest}`);

      vscode.window.showInformationMessage(
        "Nightgauge Codex commands installed. Try /nightgauge-issue-pickup."
      );
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`✗ Installation failed: ${errorMessage}`);
      vscode.window.showErrorMessage(`Failed to install Codex commands: ${errorMessage}`);
    }
  }

  /**
   * Development only: install ~/.codex/skills from the open workspace's
   * `skills/`, behind workspace trust and a modal confirmation.
   */
  async installSkillsFromWorkspace(): Promise<void> {
    await installWorkspaceSkills("Codex", path.join(codexHome(), "skills"), this.outputChannel);
  }

  dispose(): void {
    this.outputChannel.dispose();
  }
}
