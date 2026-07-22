/**
 * Centralized environment variable resolution utilities.
 *
 * Handles reading env vars and expanding `env:VAR_NAME` placeholders
 * found in config values.
 *
 * @see Issue #2742 - Extract incrediConfig.ts resolver classes
 */

export class EnvVarResolver {
  /**
   * Get environment variable value or return null.
   * Returns null if the variable is not set or is empty.
   */
  static get(varName: string): string | null {
    const value = process.env[varName];
    return value || null;
  }

  /**
   * Expand `env:VAR_NAME` placeholders in config values.
   * Returns the raw value as-is if it's not an env placeholder.
   * Returns null if the env var is not set or is empty.
   *
   * @example
   * EnvVarResolver.expandPlaceholder("env:MY_PAT")   // → process.env.MY_PAT or null
   * EnvVarResolver.expandPlaceholder("ghp_abc123")    // → "ghp_abc123"
   */
  static expandPlaceholder(value: string): string | null {
    if (!value.startsWith("env:")) return value;
    const varName = value.slice(4).trim();
    if (!varName) return null;
    const expanded = process.env[varName];
    return expanded || null;
  }

  /**
   * Check if a value is an env var placeholder (starts with "env:").
   */
  static isPlaceholder(value: string): boolean {
    return value.startsWith("env:");
  }
}
