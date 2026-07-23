/**
 * Migrate Config command
 *
 * Migrates the legacy .nightgauge/nightgauge.yaml config file to the new
 * .nightgauge/config.yaml location. Creates a backup before migration.
 *
 * @see Issue #433 - Rename config file from nightgauge.yaml to config.yaml
 */

import * as vscode from "vscode";
import * as fs from "fs/promises";
import * as path from "path";
import {
  getConfigPaths,
  needsMigration,
  CONFIG_FILE_NAME,
  LEGACY_CONFIG_FILE_NAME,
} from "../utils/configPathResolver";

/**
 * Result of migration operation
 */
export interface MigrationResult {
  success: boolean;
  backupPath?: string;
  error?: string;
}

/** Move the historical plaintext Gemini setting into OS-backed storage. */
export async function migrateLegacyGeminiApiKey(secretStore: {
  setApiKey(name: "gemini", value: string): Promise<void>;
}): Promise<void> {
  const config = vscode.workspace.getConfiguration("nightgauge");
  const inspected = config.inspect<string>("gemini.apiKey");
  const value =
    inspected?.workspaceFolderValue ?? inspected?.workspaceValue ?? inspected?.globalValue;
  if (value) await secretStore.setApiKey("gemini", value);
  for (const target of [
    vscode.ConfigurationTarget.WorkspaceFolder,
    vscode.ConfigurationTarget.Workspace,
    vscode.ConfigurationTarget.Global,
  ]) {
    try {
      await config.update("gemini.apiKey", undefined, target);
    } catch {
      // A scope can be unavailable when no folder/workspace is open.
    }
  }
}

/**
 * Migrate legacy config file to new location
 *
 * @param workspaceRoot - Workspace root path
 * @returns Migration result with success status and backup path
 */
export async function migrateConfigFile(workspaceRoot: string): Promise<MigrationResult> {
  const paths = getConfigPaths(workspaceRoot);

  // Check if migration is needed
  const shouldMigrate = await needsMigration(workspaceRoot);
  if (!shouldMigrate) {
    return {
      success: true,
      // No migration needed - either primary exists or legacy doesn't exist
    };
  }

  try {
    // Create backup of legacy file
    const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
    const backupPath = path.join(
      path.dirname(paths.legacy),
      `${LEGACY_CONFIG_FILE_NAME}.backup-${timestamp}`
    );

    // Copy to backup location
    await fs.copyFile(paths.legacy, backupPath);

    // Rename legacy to primary
    await fs.rename(paths.legacy, paths.primary);

    return {
      success: true,
      backupPath,
    };
  } catch (error) {
    return {
      success: false,
      error: error instanceof Error ? error.message : "Unknown error",
    };
  }
}

/**
 * Check if legacy config exists and prompt user for migration
 *
 * @param workspaceRoot - Workspace root path
 * @returns True if migration was performed
 */
export async function promptForMigration(workspaceRoot: string): Promise<boolean> {
  const shouldMigrate = await needsMigration(workspaceRoot);
  if (!shouldMigrate) {
    return false;
  }

  const choice = await vscode.window.showInformationMessage(
    `Nightgauge config file has been renamed from '${LEGACY_CONFIG_FILE_NAME}' to '${CONFIG_FILE_NAME}'. Would you like to migrate now?`,
    "Migrate Now",
    "Keep Using Legacy",
    "Don't Ask Again"
  );

  if (choice === "Migrate Now") {
    const result = await migrateConfigFile(workspaceRoot);
    if (result.success) {
      vscode.window.showInformationMessage(
        `Config migrated successfully. Backup saved to: ${result.backupPath}`
      );
      return true;
    }
    vscode.window.showErrorMessage(`Failed to migrate config: ${result.error}`);
    return false;
  }

  if (choice === "Don't Ask Again") {
    // Store preference to skip future prompts
    const config = vscode.workspace.getConfiguration("nightgauge");
    await config.update("skipLegacyConfigMigrationPrompt", true, vscode.ConfigurationTarget.Global);
  }

  return false;
}

/**
 * Register the Migrate Config command
 *
 * This command can be invoked from:
 * 1. Command palette: "Nightgauge: Migrate Config File"
 * 2. Prompted on extension activation when legacy file detected
 */
export function registerMigrateConfigCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.migrateConfig", async () => {
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;

    if (!workspaceRoot) {
      vscode.window.showWarningMessage("No workspace folder open. Please open a repository first.");
      return;
    }

    const shouldMigrate = await needsMigration(workspaceRoot);
    if (!shouldMigrate) {
      vscode.window.showInformationMessage(
        `No migration needed. Config file is already at ${CONFIG_FILE_NAME}.`
      );
      return;
    }

    const confirm = await vscode.window.showWarningMessage(
      `This will rename ${LEGACY_CONFIG_FILE_NAME} to ${CONFIG_FILE_NAME}. A backup will be created. Continue?`,
      "Migrate",
      "Cancel"
    );

    if (confirm !== "Migrate") {
      return;
    }

    const result = await migrateConfigFile(workspaceRoot);

    if (result.success) {
      vscode.window.showInformationMessage(
        `Config migrated successfully!\n\nBackup: ${result.backupPath}`
      );
    } else {
      vscode.window.showErrorMessage(`Failed to migrate config: ${result.error}`);
    }
  });
}
