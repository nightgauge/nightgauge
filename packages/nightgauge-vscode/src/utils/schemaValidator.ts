/**
 * Centralized value validation and type coercion utilities.
 *
 * Handles coercing string values from YAML config and env vars
 * to typed values (numbers, booleans, enums).
 *
 * @see Issue #2742 - Extract incrediConfig.ts resolver classes
 */

export class SchemaValidator {
  /**
   * Validate a string value against a set of valid options.
   * Returns the value if valid, null otherwise.
   *
   * @example
   * SchemaValidator.validateEnum("sonnet", ["sonnet", "opus", "haiku"]) // → "sonnet"
   * SchemaValidator.validateEnum("invalid", ["sonnet", "opus", "haiku"]) // → null
   */
  static validateEnum<T extends string>(value: string, validOptions: readonly T[]): T | null {
    if (validOptions.includes(value as T)) {
      return value as T;
    }
    return null;
  }

  /**
   * Coerce a string value to a number. Returns null if invalid or out of range.
   *
   * @param value - The string value to coerce
   * @param options - Validation options: min, max, integer
   *
   * @example
   * SchemaValidator.coerceNumber("42", { min: 0, integer: true }) // → 42
   * SchemaValidator.coerceNumber("abc") // → null
   * SchemaValidator.coerceNumber("-1", { min: 0 }) // → null
   */
  static coerceNumber(
    value: string,
    options?: { min?: number; max?: number; integer?: boolean }
  ): number | null {
    const parsed = options?.integer ? Number.parseInt(value, 10) : Number.parseFloat(value);
    if (Number.isNaN(parsed)) return null;
    if (options?.min !== undefined && parsed < options.min) return null;
    if (options?.max !== undefined && parsed > options.max) return null;
    return parsed;
  }

  /**
   * Coerce a string value to a boolean.
   * Returns null if not "true" or "false".
   */
  static coerceBoolean(value: string): boolean | null {
    if (value === "true") return true;
    if (value === "false") return false;
    return null;
  }

  /**
   * Strip surrounding single or double quotes from a config value.
   *
   * @example
   * SchemaValidator.stripQuotes("'interactive'") // → "interactive"
   * SchemaValidator.stripQuotes('"headless"')     // → "headless"
   * SchemaValidator.stripQuotes("plain")          // → "plain"
   */
  static stripQuotes(value: string): string {
    const trimmed = value.trim();
    if (
      (trimmed.startsWith("'") && trimmed.endsWith("'")) ||
      (trimmed.startsWith('"') && trimmed.endsWith('"'))
    ) {
      return trimmed.slice(1, -1);
    }
    return trimmed;
  }
}
