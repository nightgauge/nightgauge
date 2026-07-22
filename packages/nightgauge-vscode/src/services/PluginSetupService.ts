/**
 * PluginSetupService - Claude Code Plugin Installation Management
 *
 * Checks for Claude Code CLI and plugin installation status,
 * prompts users to install missing plugins similar to the `gh` setup flow.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import * as vscode from "vscode";
import * as path from "path";
import * as fs from "fs";
import { exec, execFile } from "child_process";
import { promisify } from "util";
import { ConfigBridge } from "./ConfigBridge";
import { type UIPluginsConfig, DEFAULT_CONFIG } from "../config/schema";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

/**
 * Plugin config interface for internal use
 */
interface PluginConfig {
  autoPrompt: boolean;
  marketplaceUrl: string;
}

/**
 * Get plugin configuration from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
function getPluginConfig(): PluginConfig {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for plugins");
    const defaults = DEFAULT_CONFIG.ui!.plugins!;
    return {
      autoPrompt: defaults.auto_prompt!,
      marketplaceUrl: defaults.marketplace_url!,
    };
  }

  const ui = configBridge.getUI();
  const plugins = ui?.plugins;
  const defaults = DEFAULT_CONFIG.ui!.plugins!;

  return {
    autoPrompt: plugins?.auto_prompt ?? defaults.auto_prompt!,
    marketplaceUrl: plugins?.marketplace_url ?? defaults.marketplace_url!,
  };
}

/**
 * Plugin installation status
 */
export interface PluginStatus {
  claudeCliAvailable: boolean;
  marketplaceAdded: boolean;
  incrediPluginInstalled: boolean;
  smartSetupPluginInstalled: boolean;
  docsPluginInstalled: boolean;
}

/**
 * Marketplace configuration
 */
export interface MarketplaceConfig {
  repoUrl: string;
  marketplaceName: string;
  plugins: string[];
}

/**
 * Default marketplace configuration for Nightgauge
 */
const DEFAULT_MARKETPLACE: MarketplaceConfig = {
  repoUrl: "https://github.com/nightgauge/nightgauge.git",
  marketplaceName: "nightgauge-plugins",
  plugins: ["nightgauge", "smart-setup", "docs"],
};

/**
 * PluginSetupService - Manages Claude Code plugin installation
 *
 * @example
 * ```typescript
 * const service = new PluginSetupService(context);
 * await service.checkAndPromptSetup();
 * ```
 */
export class PluginSetupService implements vscode.Disposable {
  private context: vscode.ExtensionContext;
  private marketplace: MarketplaceConfig;
  private outputChannel: vscode.OutputChannel;

  // State key for "don't show again"
  private static readonly DISMISSED_KEY = "nightgauge.pluginSetup.dismissed";
  private static readonly INSTALLED_KEY = "nightgauge.pluginSetup.installed";

  constructor(
    context: vscode.ExtensionContext,
    marketplace: MarketplaceConfig = DEFAULT_MARKETPLACE
  ) {
    this.context = context;
    this.marketplace = marketplace;
    this.outputChannel = vscode.window.createOutputChannel("Nightgauge Plugin Setup");
  }

  /**
   * Check plugin status and prompt user if setup is needed
   *
   * This is the main entry point - call from extension activation.
   */
  async checkAndPromptSetup(): Promise<void> {
    // Check if auto-prompt is disabled in settings
    const config = getPluginConfig();
    if (!config.autoPrompt) {
      return;
    }

    // Update marketplace URL from config
    this.marketplace.repoUrl = config.marketplaceUrl;

    // Check if user dismissed the prompt
    const dismissed = this.context.globalState.get<boolean>(
      PluginSetupService.DISMISSED_KEY,
      false
    );

    if (dismissed) {
      return;
    }

    // Check if already installed in a previous session
    const installed = this.context.globalState.get<boolean>(
      PluginSetupService.INSTALLED_KEY,
      false
    );

    if (installed) {
      // Verify it's still installed
      const status = await this.getPluginStatus();
      if (status.incrediPluginInstalled) {
        return;
      }
      // Reset if somehow uninstalled
      await this.context.globalState.update(PluginSetupService.INSTALLED_KEY, false);
    }

    const status = await this.getPluginStatus();

    if (!status.claudeCliAvailable) {
      await this.promptClaudeCliInstall();
      return;
    }

    if (!status.incrediPluginInstalled) {
      await this.promptPluginInstall(status);
    }
  }

  /**
   * Get current plugin installation status
   */
  async getPluginStatus(): Promise<PluginStatus> {
    const status: PluginStatus = {
      claudeCliAvailable: false,
      marketplaceAdded: false,
      incrediPluginInstalled: false,
      smartSetupPluginInstalled: false,
      docsPluginInstalled: false,
    };

    // Check if Claude CLI is available
    try {
      await execAsync("claude --version");
      status.claudeCliAvailable = true;
    } catch {
      return status;
    }

    // Check installed marketplaces
    try {
      const { stdout } = await execAsync("claude plugin marketplace list");
      status.marketplaceAdded = stdout.includes(this.marketplace.marketplaceName);
    } catch {
      // Marketplace command failed, assume not available
    }

    // Check installed plugins
    try {
      const { stdout } = await execAsync("claude plugin list");
      status.incrediPluginInstalled = stdout.includes("nightgauge");
      status.smartSetupPluginInstalled = stdout.includes("smart-setup");
      status.docsPluginInstalled = stdout.includes("docs");
    } catch {
      // Plugin list failed
    }

    return status;
  }

  /**
   * Prompt user to install Claude CLI
   */
  private async promptClaudeCliInstall(): Promise<void> {
    const selection = await vscode.window.showWarningMessage(
      "Claude Code CLI is required for Nightgauge Pipeline. Would you like to install it?",
      { modal: true },
      "Open Installation Guide",
      "Later",
      "Don't Show Again"
    );

    switch (selection) {
      case "Open Installation Guide":
        vscode.env.openExternal(
          vscode.Uri.parse("https://docs.anthropic.com/en/docs/claude-code/getting-started")
        );
        break;
      case "Don't Show Again":
        await this.context.globalState.update(PluginSetupService.DISMISSED_KEY, true);
        break;
    }
  }

  /**
   * Prompt user to install Nightgauge plugins
   */
  private async promptPluginInstall(status: PluginStatus): Promise<void> {
    const message = status.marketplaceAdded
      ? "Nightgauge plugins are available but not installed. Install them for the full pipeline experience?"
      : "Nightgauge plugins enhance your development workflow. Would you like to install them?";

    const selection = await vscode.window.showInformationMessage(
      message,
      "Install Plugins",
      "Later",
      "Don't Show Again"
    );

    switch (selection) {
      case "Install Plugins":
        await this.installPlugins(status);
        break;
      case "Don't Show Again":
        await this.context.globalState.update(PluginSetupService.DISMISSED_KEY, true);
        break;
    }
  }

  /**
   * Get the path to the bundled marketplace in the extension's dist/ directory.
   * Returns undefined if the bundled marketplace is not available.
   */
  private getBundledMarketplacePath(): string | undefined {
    const marketplacePath = path.join(
      this.context.extensionPath,
      "dist",
      ".claude-plugin",
      "marketplace.json"
    );
    if (fs.existsSync(marketplacePath)) {
      // Return the directory containing .claude-plugin/ (i.e., dist/)
      return path.join(this.context.extensionPath, "dist");
    }
    return undefined;
  }

  /**
   * Install marketplace and plugins
   */
  private async installPlugins(status: PluginStatus): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting Nightgauge plugin installation...\n");

    try {
      // Add marketplace if needed
      if (!status.marketplaceAdded) {
        // Prefer the bundled marketplace (ships with the VSIX) over git clone
        const bundledPath = this.getBundledMarketplacePath();
        const marketplaceSource = bundledPath ?? this.marketplace.repoUrl;
        const sourceLabel = bundledPath ? "bundled" : "remote";

        this.outputChannel.appendLine(`Adding marketplace (${sourceLabel}): ${marketplaceSource}`);

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: "Adding Nightgauge marketplace...",
            cancellable: false,
          },
          async () => {
            await execFileAsync("claude", ["plugin", "marketplace", "add", marketplaceSource]);
          }
        );

        this.outputChannel.appendLine("✓ Marketplace added\n");
      }

      // Install each plugin
      const pluginsToInstall = this.marketplace.plugins.filter((plugin) => {
        switch (plugin) {
          case "nightgauge":
            return !status.incrediPluginInstalled;
          case "smart-setup":
            return !status.smartSetupPluginInstalled;
          case "docs":
            return !status.docsPluginInstalled;
          default:
            return true;
        }
      });

      for (const plugin of pluginsToInstall) {
        this.outputChannel.appendLine(`Installing plugin: ${plugin}`);

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Installing ${plugin} plugin...`,
            cancellable: false,
          },
          async () => {
            await execAsync(`claude plugin install ${plugin}@${this.marketplace.marketplaceName}`);
          }
        );

        this.outputChannel.appendLine(`✓ ${plugin} installed\n`);
      }

      // Mark as installed
      await this.context.globalState.update(PluginSetupService.INSTALLED_KEY, true);

      this.outputChannel.appendLine("All plugins installed successfully!");
      this.outputChannel.appendLine("\nAvailable commands:");
      this.outputChannel.appendLine("  /nightgauge:issue-pickup     - Pick up an issue");
      this.outputChannel.appendLine("  /nightgauge:feature-planning - Plan the implementation");
      this.outputChannel.appendLine("  /nightgauge:feature-dev      - Implement the feature");
      this.outputChannel.appendLine("  /nightgauge:pr-create        - Create a pull request");
      this.outputChannel.appendLine("  /nightgauge:pr-merge         - Merge the PR");

      vscode.window
        .showInformationMessage(
          "Nightgauge plugins installed! Use /nightgauge:issue-pickup to start.",
          "Show Commands"
        )
        .then((selection) => {
          if (selection === "Show Commands") {
            this.outputChannel.show(true);
          }
        });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.outputChannel.appendLine(`\n✗ Installation failed: ${errorMessage}`);

      vscode.window
        .showErrorMessage(`Failed to install plugins: ${errorMessage}`, "View Output")
        .then((selection) => {
          if (selection === "View Output") {
            this.outputChannel.show(true);
          }
        });
    }
  }

  /**
   * Reset the "don't show again" state (for testing or user preference)
   */
  async resetDismissed(): Promise<void> {
    await this.context.globalState.update(PluginSetupService.DISMISSED_KEY, false);
    await this.context.globalState.update(PluginSetupService.INSTALLED_KEY, false);
  }

  /**
   * Manually trigger the setup prompt
   */
  async showSetupPrompt(): Promise<void> {
    await this.resetDismissed();
    await this.checkAndPromptSetup();
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this.outputChannel.dispose();
  }
}

/**
 * Re-export UIPluginsConfig for consumers that need the raw type
 */
export type { UIPluginsConfig };
