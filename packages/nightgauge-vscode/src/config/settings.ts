/**
 * Settings accessor for Nightgauge extension
 *
 * Provides typed access to configuration via ConfigBridge (6-tier merged config).
 *
 * @see Issue #476 - Refactor to use ConfigBridge instead of direct VSCode reads
 */

import * as vscode from "vscode";
import {
  getCoreSettings,
  type ExecutionAdapter,
  type AuthProvider,
  type ModelSelection,
} from "./coreSettings";
import { IpcClient } from "../services/IpcClient";

/**
 * Re-export types from coreSettings for backward compatibility
 */
export type { ExecutionAdapter, AuthProvider, ModelSelection };

/**
 * Nightgauge extension settings interface
 */
export interface IncrediSettings {
  /** Execution adapter for pipeline stage orchestration */
  executionAdapter: ExecutionAdapter;
  /** Authentication provider for Claude API */
  authProvider: AuthProvider;
  /** Default model for pipeline stages */
  defaultModel: ModelSelection;
  /** Path to context files relative to workspace root */
  contextPath: string;
  /** Path to plan files relative to workspace root */
  plansPath: string;
}

/**
 * Default settings values
 *
 * @deprecated Use DEFAULT_CORE_SETTINGS from coreSettings.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_SETTINGS: IncrediSettings = {
  executionAdapter: "claude",
  authProvider: "max",
  defaultModel: "sonnet",
  contextPath: ".nightgauge/pipeline",
  plansPath: ".nightgauge/plans",
};

/**
 * Get current Nightgauge settings from ConfigBridge (6-tier merged config)
 *
 * @see Issue #476 - Now uses ConfigBridge instead of direct VSCode reads
 */
export function getSettings(): IncrediSettings {
  const coreSettings = getCoreSettings();

  return {
    executionAdapter: coreSettings.executionAdapter,
    authProvider: coreSettings.authProvider,
    defaultModel: coreSettings.defaultModel,
    contextPath: coreSettings.contextPath,
    plansPath: coreSettings.plansPath,
  };
}

/**
 * Get absolute path to context directory for the current workspace
 */
export function getContextPath(): string | undefined {
  const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
  if (!workspaceFolder) {
    return undefined;
  }

  const settings = getSettings();
  return vscode.Uri.joinPath(workspaceFolder.uri, settings.contextPath).fsPath;
}

/**
 * Get absolute path to plans directory for the current workspace
 */
export function getPlansPath(): string | undefined {
  const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
  if (!workspaceFolder) {
    return undefined;
  }

  const settings = getSettings();
  return vscode.Uri.joinPath(workspaceFolder.uri, settings.plansPath).fsPath;
}

/**
 * Get the current workspace root path
 */
export function getWorkspaceRoot(): string | undefined {
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
}

/**
 * Get the git repository root path.
 *
 * This ensures pipeline files are always stored at the repository root,
 * even when VSCode workspace is opened in a subdirectory.
 *
 * @param workspaceRoot - The current workspace root path
 * @returns The git root path, or null if not in a git repository
 */
export async function getGitRoot(workspaceRoot: string): Promise<string | null> {
  try {
    const ipc = IpcClient.getInstance();
    return await ipc.gitRoot(workspaceRoot);
  } catch {
    return null;
  }
}

/**
 * Get the effective root path for .nightgauge directory.
 *
 * Prefers git root, falls back to workspace root with a warning.
 * This ensures consistent file placement across all pipeline operations.
 *
 * @param workspaceRoot - The current workspace root path
 * @returns The effective root path for .nightgauge files
 */
export async function getIncrediRoot(workspaceRoot: string): Promise<string> {
  const gitRoot = await getGitRoot(workspaceRoot);

  if (gitRoot) {
    if (gitRoot !== workspaceRoot) {
      console.log(
        `[Nightgauge] Using git root for .nightgauge directory: ${gitRoot} (workspace is ${workspaceRoot})`
      );
    }
    return gitRoot;
  }

  console.warn(`[Nightgauge] Not in a git repository. Using workspace root: ${workspaceRoot}`);
  return workspaceRoot;
}

// Re-export notification settings for convenience
export {
  getNotificationSettings,
  type NotificationSettings,
  type NotificationType,
} from "./notificationSettings";

// Re-export limits settings
export { getLimitsSettings, type LimitsSettings } from "./limitsSettings";

// Re-export work-item source settings
export { getWorkItemSourceConfig, type WorkItemSourceConfig } from "./workItemSourceSettings";
