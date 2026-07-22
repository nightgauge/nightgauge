/**
 * RecommendationApplier - Applies actionable recommendations to config
 *
 * Maps recommendation categories to config patches, applies them via
 * IncrediYamlService, and manages a 30-second revert window.
 *
 * @see Issue #787 - Actionable Dashboard Recommendations
 */

import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { IncrediConfig } from "../config/schema";

interface RevertState {
  configPath: string;
  previousValue: unknown;
  appliedAt: number;
  timer: ReturnType<typeof setTimeout>;
}

export interface ApplyResult {
  success: boolean;
  error?: string;
  previousValue?: unknown;
}

export interface RevertResult {
  success: boolean;
  error?: string;
}

const REVERT_WINDOW_MS = 30_000;

export class RecommendationApplier {
  private readonly yamlService: IncrediYamlService;
  private revertState: Map<string, RevertState> = new Map();
  private appliedCategories: Set<string> = new Set();

  constructor(workspaceRoot: string) {
    this.yamlService = new IncrediYamlService(workspaceRoot);
  }

  async apply(category: string, configPath: string, value: unknown): Promise<ApplyResult> {
    try {
      // Read current config
      const readResult = await this.yamlService.read();
      const currentConfig: IncrediConfig = readResult.config ?? {};

      // Get current value at path for revert
      const previousValue = getNestedValue(currentConfig, configPath);

      // Build partial config from dot-notation path
      const patch = buildPartialConfig(configPath, value);

      // Merge patch into current config
      const mergedConfig = deepMerge(currentConfig, patch);

      // Write merged config
      const writeResult = await this.yamlService.write(mergedConfig, "project");
      if (!writeResult.success) {
        return {
          success: false,
          error: writeResult.error ?? "Failed to write config",
        };
      }

      // Clear any existing revert state for this category
      this.clearRevertState(category);

      // Set up revert window
      const timer = setTimeout(() => {
        this.revertState.delete(category);
      }, REVERT_WINDOW_MS);

      this.revertState.set(category, {
        configPath,
        previousValue,
        appliedAt: Date.now(),
        timer,
      });

      this.appliedCategories.add(category);

      return { success: true, previousValue };
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      return { success: false, error: message };
    }
  }

  async revert(category: string): Promise<RevertResult> {
    const state = this.revertState.get(category);
    if (!state) {
      return {
        success: false,
        error: "No revert state available (window may have expired)",
      };
    }

    try {
      // Read current config
      const readResult = await this.yamlService.read();
      const currentConfig: IncrediConfig = readResult.config ?? {};

      // Build patch with original value
      const patch = buildPartialConfig(state.configPath, state.previousValue);
      const mergedConfig = deepMerge(currentConfig, patch);

      // Write reverted config
      const writeResult = await this.yamlService.write(mergedConfig, "project");
      if (!writeResult.success) {
        return {
          success: false,
          error: writeResult.error ?? "Failed to write config",
        };
      }

      // Clean up
      this.clearRevertState(category);
      this.appliedCategories.delete(category);

      return { success: true };
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      return { success: false, error: message };
    }
  }

  getAppliedCategories(): string[] {
    return [...this.appliedCategories];
  }

  canRevert(category: string): boolean {
    return this.revertState.has(category);
  }

  private clearRevertState(category: string): void {
    const existing = this.revertState.get(category);
    if (existing) {
      clearTimeout(existing.timer);
      this.revertState.delete(category);
    }
  }

  dispose(): void {
    for (const state of this.revertState.values()) {
      clearTimeout(state.timer);
    }
    this.revertState.clear();
    this.yamlService.dispose();
  }
}

function getNestedValue(obj: Record<string, unknown>, path: string): unknown {
  const parts = path.split(".");
  let current: unknown = obj;
  for (const part of parts) {
    if (current === null || current === undefined || typeof current !== "object") {
      return undefined;
    }
    current = (current as Record<string, unknown>)[part];
  }
  return current;
}

function buildPartialConfig(path: string, value: unknown): Record<string, unknown> {
  const parts = path.split(".");
  const result: Record<string, unknown> = {};
  let current = result;

  for (let i = 0; i < parts.length - 1; i++) {
    current[parts[i]] = {};
    current = current[parts[i]] as Record<string, unknown>;
  }

  current[parts[parts.length - 1]] = value;
  return result;
}

function deepMerge<T extends Record<string, unknown>>(
  target: T,
  source: Record<string, unknown>
): T {
  const result = { ...target } as Record<string, unknown>;

  for (const key in source) {
    const sourceValue = source[key];
    const targetValue = target[key];

    if (sourceValue === undefined) {
      continue;
    }

    if (
      typeof sourceValue === "object" &&
      sourceValue !== null &&
      !Array.isArray(sourceValue) &&
      typeof targetValue === "object" &&
      targetValue !== null &&
      !Array.isArray(targetValue)
    ) {
      result[key] = deepMerge(
        targetValue as Record<string, unknown>,
        sourceValue as Record<string, unknown>
      );
    } else {
      result[key] = sourceValue;
    }
  }

  return result as T;
}
