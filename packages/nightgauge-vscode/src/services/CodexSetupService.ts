/**
 * CodexSetupService - Codex CLI command/skill installation management
 *
 * Installs Nightgauge Codex slash commands into ~/.codex/commands and, when
 * available in the workspace, installs Nightgauge skills into ~/.codex/skills.
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { exec } from "child_process";
import { promisify } from "util";

const execAsync = promisify(exec);

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
    this.outputChannel = vscode.window.createOutputChannel("Nightgauge Codex Setup");
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

    const codexHome = process.env.CODEX_HOME || path.join(os.homedir(), ".codex");
    const commandsDir = path.join(codexHome, "commands");
    const commandsInstalled = REQUIRED_COMMANDS.every((file) =>
      fs.existsSync(path.join(commandsDir, file))
    );

    return { codexCliAvailable, commandsInstalled };
  }

  private resolveCommandsSourceDir(): string | null {
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    const workspaceSource = workspaceRoot ? path.join(workspaceRoot, ".codex", "commands") : null;
    if (workspaceSource && fs.existsSync(workspaceSource)) {
      return workspaceSource;
    }

    const bundledSource = path.join(this.context.extensionPath, "resources", "codex", "commands");
    if (fs.existsSync(bundledSource)) {
      return bundledSource;
    }

    return null;
  }

  private async installAssets(): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting Codex asset installation...\n");

    try {
      const sourceCommandsDir = this.resolveCommandsSourceDir();
      if (!sourceCommandsDir) {
        throw new Error("No Codex command source found (.codex/commands or bundled resources).");
      }

      const codexHome = process.env.CODEX_HOME || path.join(os.homedir(), ".codex");
      const commandsDest = path.join(codexHome, "commands");
      await fs.promises.mkdir(commandsDest, { recursive: true });

      for (const file of REQUIRED_COMMANDS) {
        await fs.promises.copyFile(
          path.join(sourceCommandsDir, file),
          path.join(commandsDest, file)
        );
      }

      // Best-effort skill sync from workspace (if present).
      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      if (workspaceRoot) {
        const sourceSkillsDir = path.join(workspaceRoot, "skills");
        const skillsDest = path.join(codexHome, "skills");
        if (fs.existsSync(sourceSkillsDir)) {
          await fs.promises.mkdir(skillsDest, { recursive: true });
          const entries = await fs.promises.readdir(sourceSkillsDir, {
            withFileTypes: true,
          });
          for (const entry of entries) {
            if (!entry.isDirectory()) continue;
            const sourceDir = path.join(sourceSkillsDir, entry.name);
            const skillFile = path.join(sourceDir, "SKILL.md");
            if (!fs.existsSync(skillFile)) continue;

            const targetDir = path.join(skillsDest, entry.name);
            await fs.promises.rm(targetDir, { recursive: true, force: true });
            await fs.promises.cp(sourceDir, targetDir, { recursive: true });
          }
        }
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

  dispose(): void {
    this.outputChannel.dispose();
  }
}
