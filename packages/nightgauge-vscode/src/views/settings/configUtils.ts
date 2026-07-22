/**
 * Config Utilities - Pure functions for .nightgauge/config.yaml configuration
 *
 * This file contains pure functions for validation, merging, and accessing
 * configuration values. These functions have no dependencies on the VSCode API
 * and can be tested in isolation.
 *
 * Validation now uses Zod schemas from config/schema.ts.
 *
 * @see config/schema.ts for the Zod schema (source of truth)
 * @see Issue #432 - Comprehensive Zod Schema for Config Fields
 */

import type { IncrediConfig, ValidationResult } from "./types";
import {
  validateConfig as zodValidateConfig,
  mergeWithDefaults as zodMergeWithDefaults,
  type ConfigValidationResult,
} from "../../config/schema";

/**
 * Validate a IncrediConfig object using Zod schema
 *
 * @param config - Configuration to validate
 * @returns Validation result with any errors (legacy format for backward compatibility)
 */
export function validateConfig(config: IncrediConfig): ValidationResult {
  const result: ConfigValidationResult = zodValidateConfig(config);

  return {
    valid: result.valid,
    errors: result.errors.map((e) => ({
      field: e.field,
      message: e.message,
    })),
  };
}

/**
 * Merge user config with defaults using Zod schema defaults
 *
 * @param config - User configuration (partial)
 * @returns Complete configuration with defaults applied
 */
export function mergeWithDefaults(config: IncrediConfig): IncrediConfig {
  return zodMergeWithDefaults(config);
}

/**
 * Deep merge two objects
 *
 * @deprecated Use mergeWithDefaults which applies Zod defaults instead
 */
function deepMerge<T extends object>(target: T, source: Partial<T>): T {
  const result = { ...target };

  for (const key in source) {
    const sourceValue = source[key];
    const targetValue = (target as Record<string, unknown>)[key];

    if (sourceValue === undefined) {
      continue;
    }

    if (isObject(sourceValue) && isObject(targetValue) && !Array.isArray(sourceValue)) {
      (result as Record<string, unknown>)[key] = deepMerge(
        targetValue as object,
        sourceValue as object
      );
    } else {
      (result as Record<string, unknown>)[key] = sourceValue;
    }
  }

  return result;
}

/**
 * Type guard for objects
 */
function isObject(value: unknown): value is object {
  return typeof value === "object" && value !== null;
}

/**
 * Remove undefined values from an object recursively
 */
export function removeUndefined<T extends object>(obj: T): T {
  const result: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(obj)) {
    if (value === undefined) {
      continue;
    }

    if (isObject(value) && !Array.isArray(value)) {
      const cleaned = removeUndefined(value as object);
      if (Object.keys(cleaned).length > 0) {
        result[key] = cleaned;
      }
    } else {
      result[key] = value;
    }
  }

  return result as T;
}

/**
 * Get a nested value from config by dot-notation path
 *
 * @param config - Configuration object
 * @param path - Dot-notation path (e.g., 'project.number')
 * @returns The value at the path, or undefined
 */
export function getConfigValue(config: IncrediConfig, path: string): unknown {
  const parts = path.split(".");
  let current: unknown = config;

  for (const part of parts) {
    if (current === null || current === undefined) {
      return undefined;
    }
    current = (current as Record<string, unknown>)[part];
  }

  return current;
}

/**
 * Set a nested value in config by dot-notation path
 *
 * @param config - Configuration object (mutated)
 * @param path - Dot-notation path (e.g., 'project.number')
 * @param value - Value to set
 */
export function setConfigValue(config: IncrediConfig, path: string, value: unknown): void {
  const parts = path.split(".");
  let current: Record<string, unknown> = config as Record<string, unknown>;

  for (let i = 0; i < parts.length - 1; i++) {
    const part = parts[i];
    if (current[part] === undefined || current[part] === null) {
      current[part] = {};
    }
    current = current[part] as Record<string, unknown>;
  }

  const lastPart = parts[parts.length - 1];
  current[lastPart] = value;
}
