/**
 * PluginSetupService - Claude Code Plugin Installation Management
 *
 * Checks for Claude Code CLI and plugin installation status,
 * prompts users to install missing plugins similar to the `gh` setup flow.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import * as vscode from "vscode";
import { execFile } from "child_process";
import { promisify } from "util";
import { ConfigBridge } from "./ConfigBridge";
import { type UIPluginsConfig, DEFAULT_CONFIG } from "../config/schema";
import { getPrefixedMainChannel } from "../utils/logger";
import {
  ensureStableMarketplace,
  isVersionKeyedSource,
  type StableMarketplace,
} from "./stableMarketplace";

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
 * What `claude plugin list --json` says about the plugin. `failed` wins over
 * `enabled`: during an outage an entry reads `"enabled": true` beside
 * `"errors": ["Marketplace nightgauge-plugins failed to load: cache-miss"]`.
 * `unknown` means the output could not be read, which is evidence of nothing.
 */
export type PluginState = "enabled" | "disabled" | "failed" | "absent" | "unknown";

/**
 * Plugin installation status
 */
export interface PluginStatus {
  claudeCliAvailable: boolean;
  marketplaceAdded: boolean;
  /** The registered source directory; undefined for a git source. */
  marketplacePath?: string;
  plugin: PluginState;
  /** Claude Code's own error text, verbatim. */
  pluginErrors: string[];
  /** Why `plugin` is `unknown`. Unset whenever the list was read. */
  pluginReadError?: string;
}

interface PluginListEntry {
  id?: unknown;
  enabled?: unknown;
  errors?: unknown;
}

interface MarketplaceListEntry {
  name?: unknown;
  source?: unknown;
  path?: unknown;
}

/** Read one plugin's state from `claude plugin list --json`, by exact id. */
export function parsePluginState(
  stdout: string,
  pluginId: string
): Pick<PluginStatus, "plugin" | "pluginErrors"> {
  let entries: unknown;
  try {
    entries = JSON.parse(stdout);
  } catch {
    return { plugin: "unknown", pluginErrors: [] };
  }
  if (!Array.isArray(entries)) {
    return { plugin: "unknown", pluginErrors: [] };
  }

  // The same id appears once per scope it is installed in.
  const matches = (entries as PluginListEntry[]).filter((entry) => entry?.id === pluginId);
  if (matches.length === 0) {
    return { plugin: "absent", pluginErrors: [] };
  }
  const pluginErrors = matches.flatMap((entry) =>
    Array.isArray(entry.errors) ? entry.errors.map(String) : []
  );
  if (pluginErrors.length > 0) {
    return { plugin: "failed", pluginErrors };
  }
  const enabled = matches.some((entry) => entry.enabled === true);
  return { plugin: enabled ? "enabled" : "disabled", pluginErrors };
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
  repoUrl: "https://github.com/nightgauge/nightgauge.git#stable",
  marketplaceName: "nightgauge-plugins",
  plugins: ["nightgauge"],
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

  constructor(
    context: vscode.ExtensionContext,
    marketplace: MarketplaceConfig = DEFAULT_MARKETPLACE
  ) {
    this.context = context;
    this.marketplace = marketplace;
    // Folded into the shared main channel behind a "plugin-setup" tag rather
    // than its own destination — retired six-channel split (#749).
    this.outputChannel = getPrefixedMainChannel("plugin-setup");
  }

  /**
   * Check plugin status, repair what is broken, and prompt if setup is needed
   *
   * This is the main entry point - call from extension activation. `autoPrompt`
   * and "don't show again" decline an install offer, not an outage report, so
   * repair and failure reporting run ahead of both.
   */
  async checkAndPromptSetup(): Promise<void> {
    const config = getPluginConfig();
    this.marketplace.repoUrl = config.marketplaceUrl;

    let status = await this.getPluginStatus();

    if (status.claudeCliAvailable) {
      const stable = ensureStableMarketplace(this.context, (message) =>
        this.outputChannel.appendLine(`WARNING: ${message}`)
      );
      status = await this.repairIfNeeded(status, stable);
      await this.refreshIfRecopied(status, stable);
      if (status.plugin === "unknown") {
        this.reportUnreadable(status);
      }
    }

    const dismissed = this.context.globalState.get<boolean>(
      PluginSetupService.DISMISSED_KEY,
      false
    );
    if (!config.autoPrompt || dismissed) {
      return;
    }

    if (!status.claudeCliAvailable) {
      await this.promptClaudeCliInstall();
      return;
    }

    if (status.plugin === "absent") {
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
      plugin: "unknown",
      pluginErrors: [],
    };

    try {
      await execFileAsync("claude", ["--version"]);
      status.claudeCliAvailable = true;
    } catch {
      return status;
    }

    try {
      const { stdout } = await execFileAsync("claude", ["plugin", "marketplace", "list", "--json"]);
      const entries = JSON.parse(stdout) as MarketplaceListEntry[];
      const entry = entries.find((m) => m.name === this.marketplace.marketplaceName);
      status.marketplaceAdded = entry !== undefined;
      if (entry?.source === "directory" && typeof entry.path === "string") {
        status.marketplacePath = entry.path;
      } else if (
        entry !== undefined &&
        (typeof entry.source !== "string" || entry.source === "directory")
      ) {
        // Without a path the move off a version-keyed source cannot run.
        this.outputChannel.appendLine(
          `WARNING: marketplace ${this.marketplace.marketplaceName} has no usable source: ` +
            JSON.stringify(entry)
        );
      }
    } catch (error) {
      this.logCliFailure("marketplace list", error);
    }

    try {
      const { stdout } = await execFileAsync("claude", ["plugin", "list", "--json"]);
      Object.assign(status, parsePluginState(stdout, this.pluginId()));
      if (status.plugin === "unknown") {
        status.pluginReadError = "claude plugin list --json returned unreadable output";
      }
    } catch (error) {
      this.logCliFailure("plugin list", error);
      status.pluginReadError = error instanceof Error ? error.message : String(error);
    }

    return status;
  }

  private pluginId(plugin = this.marketplace.plugins[0]): string {
    return `${plugin}@${this.marketplace.marketplaceName}`;
  }

  private logCliFailure(command: string, error: unknown): void {
    const message = error instanceof Error ? error.message : String(error);
    this.outputChannel.appendLine(`WARNING: claude ${command} failed: ${message}`);
  }

  private isVersionKeyed(status: PluginStatus): boolean {
    return (
      status.marketplacePath !== undefined &&
      isVersionKeyedSource(status.marketplacePath, this.context.extensionPath)
    );
  }

  /**
   * Repoint the marketplace when the plugin failed, or when it is enabled from a
   * directory an upgrade will delete. A healthy source elsewhere is never touched.
   *
   * `marketplace add` repoints an existing name in place and keeps the install
   * and its data; `marketplace remove` would uninstall the plugin and delete
   * them (observed on Claude Code 2.1.316). One attempt per activation.
   */
  private async repairIfNeeded(
    status: PluginStatus,
    stable: StableMarketplace | undefined
  ): Promise<PluginStatus> {
    const failed = status.plugin === "failed";
    if (!failed && !(status.plugin === "enabled" && this.isVersionKeyed(status))) {
      return status;
    }

    const source = stable?.path ?? (failed ? this.marketplace.repoUrl : undefined);
    if (!source) {
      return status;
    }

    this.outputChannel.appendLine(`Repointing marketplace to ${source}`);
    let addError: string | undefined;
    try {
      await execFileAsync("claude", ["plugin", "marketplace", "add", source]);
    } catch (error) {
      addError = error instanceof Error ? error.message : String(error);
    }

    const after = await this.getPluginStatus();
    const problems = [...after.pluginErrors, ...(addError ? [addError] : [])];
    if (problems.length > 0) {
      this.reportFailure(problems);
    } else if (after.plugin === "unknown") {
      // checkAndPromptSetup surfaces this once, for repaired and unrepaired alike.
      this.outputChannel.appendLine("Marketplace repointed; the repair could not be verified");
    } else {
      this.outputChannel.appendLine("✓ Marketplace repaired");
    }
    return after;
  }

  /** A status that could not be read is neither healthy nor absent: say so. */
  private reportUnreadable(status: PluginStatus): void {
    const detail = status.pluginReadError ?? "no plugin list was read";
    this.outputChannel.appendLine(`✗ Nightgauge plugin status unavailable: ${detail}`);
    vscode.window
      .showWarningMessage(
        `Could not read the Nightgauge Claude plugin's status: ${detail}`,
        "View Output"
      )
      .then((selection) => {
        if (selection === "View Output") {
          this.outputChannel.show(true);
        }
      });
  }

  private reportFailure(problems: string[]): void {
    const detail = problems.join("; ");
    this.outputChannel.appendLine(`✗ Nightgauge plugin repair failed: ${detail}`);
    vscode.window
      .showErrorMessage(`The Nightgauge Claude plugin failed to load: ${detail}`, "View Output")
      .then((selection) => {
        if (selection === "View Output") {
          this.outputChannel.show(true);
        }
      });
  }

  /** Tell Claude Code the registered copy changed. A failure here costs freshness only. */
  private async refreshIfRecopied(
    status: PluginStatus,
    stable: StableMarketplace | undefined
  ): Promise<void> {
    if (!stable?.recopied || status.marketplacePath !== stable.path) {
      return;
    }
    try {
      await execFileAsync("claude", [
        "plugin",
        "marketplace",
        "update",
        this.marketplace.marketplaceName,
      ]);
      await execFileAsync("claude", ["plugin", "update", this.pluginId()]);
    } catch (error) {
      this.logCliFailure("plugin update", error);
    }
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
   * Install marketplace and plugins
   */
  private async installPlugins(status: PluginStatus): Promise<void> {
    this.outputChannel.show(true);
    this.outputChannel.appendLine("Starting Nightgauge plugin installation...\n");

    try {
      // Add the marketplace, or move it off a directory an upgrade will delete
      if (!status.marketplaceAdded || this.isVersionKeyed(status)) {
        // Prefer the bundled marketplace (ships with the VSIX) over git clone
        const bundledPath = ensureStableMarketplace(this.context, (message) =>
          this.outputChannel.appendLine(`WARNING: ${message}`)
        )?.path;
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

      // `absent` is the only state that reaches this prompt, so each plugin installs.
      for (const plugin of this.marketplace.plugins) {
        this.outputChannel.appendLine(`Installing plugin: ${plugin}`);

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Installing ${plugin} plugin...`,
            cancellable: false,
          },
          async () => {
            await execFileAsync("claude", ["plugin", "install", this.pluginId(plugin)]);
          }
        );

        this.outputChannel.appendLine(`✓ ${plugin} installed\n`);
      }

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
