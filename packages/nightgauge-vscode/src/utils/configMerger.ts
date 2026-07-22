/**
 * Configuration source merging with correct priority order.
 *
 * Handles the env > config > default priority pattern used
 * throughout the nightgauge config system.
 *
 * Also provides shared config file reading utilities to eliminate
 * boilerplate across resolver modules.
 *
 * @see Issue #2742 - Extract incrediConfig.ts resolver classes
 */

import * as fs from "node:fs";
import * as vscode from "vscode";
import { resolveConfigPathSync, logDeprecationWarning } from "./configPathResolver";
import { readEffectiveConfigTextSync } from "./mergedConfigReader";

export interface ConfigSource<T = string> {
  env?: T | null;
  config?: T | null;
  default: T;
}

export class ConfigMerger {
  /**
   * Merge config sources following 6-tier precedence.
   * Priority: env > config > default.
   *
   * @example
   * ConfigMerger.merge({ env: "opus", config: "sonnet", default: "haiku" }) // → "opus"
   * ConfigMerger.merge({ env: null, config: "sonnet", default: "haiku" })   // → "sonnet"
   * ConfigMerger.merge({ config: null, default: "haiku" })                  // → "haiku"
   */
  static merge<T>(sources: ConfigSource<T>): T {
    if (sources.env != null) return sources.env;
    if (sources.config != null) return sources.config;
    return sources.default;
  }

  /**
   * Read config file lines for the given workspace root.
   * Returns null if no config exists or on read error.
   *
   * Handles:
   * - Workspace root auto-detection from vscode.workspace
   * - Config path resolution (primary + legacy fallback)
   * - Deprecation warning for legacy config paths
   * - Error handling (returns null, never throws)
   */
  static readConfigLines(workspaceRoot?: string): string[] | null {
    const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!root) return null;

    try {
      const pathResult = resolveConfigPathSync(root);
      if (!pathResult.exists) return null;
      if (pathResult.isLegacy) {
        logDeprecationWarning(pathResult.path);
      }
      return readEffectiveConfigTextSync(pathResult).split("\n");
    } catch {
      return null;
    }
  }

  /**
   * Get the resolved workspace root path.
   * Returns provided root or auto-detects from vscode.workspace.
   */
  static resolveRoot(workspaceRoot?: string): string | null {
    return workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? null;
  }
}
