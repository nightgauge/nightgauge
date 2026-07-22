/**
 * Shared adapter error types and helpers.
 *
 * Provides a single source of truth for error formatting, categories, and
 * actionable messages across all 8 CLI adapters. Every adapter-thrown error
 * is an `AdapterError` instance with a machine-readable `category` field,
 * a human-readable message following the `[Adapter Name] CATEGORY: reason`
 * template, and an optional documentation URL.
 *
 * @see Issue #2596 - Standardize adapter error messages
 * @see docs/ADAPTER_ERROR_HANDLING.md for usage guide and examples
 */

/**
 * Machine-readable error categories for adapter failures.
 */
export type AdapterErrorCategory =
  | "AUTH_MISSING"
  | "AUTH_EXPIRED"
  | "BINARY_NOT_FOUND"
  | "VERSION_MISMATCH"
  | "SERVER_UNREACHABLE"
  | "MODEL_NOT_FOUND"
  | "CONFIG_INVALID"
  | "TIMEOUT";

/**
 * Structured error thrown by all adapter auth validation and query functions.
 *
 * Provides machine-readable categorization, the adapter name, an actionable
 * message, and an optional documentation URL. Use the `format()` method to
 * produce the standardized `[Adapter Name] CATEGORY: message` string.
 *
 * @example
 * ```typescript
 * throw new AdapterError(
 *   "claude CLI is not installed or not in PATH.\nFix: brew install claude",
 *   "BINARY_NOT_FOUND",
 *   "Claude Headless",
 *   "https://docs.anthropic.com/en/docs/claude-code"
 * );
 * ```
 */
export class AdapterError extends Error {
  constructor(
    message: string,
    public readonly category: AdapterErrorCategory,
    public readonly adapterName: string,
    public readonly actionUrl?: string
  ) {
    super(message);
    this.name = "AdapterError";
    // Maintains proper stack trace in V8
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, AdapterError);
    }
  }

  /**
   * Returns the error formatted as `[Adapter Name] CATEGORY: message`.
   * Suitable for display in pipeline output and error logs.
   */
  format(): string {
    return `[${this.adapterName}] ${this.category}: ${this.message}`;
  }
}

/**
 * Throw an AUTH_MISSING error with consistent formatting.
 *
 * @param adapterName - Human-readable adapter name (e.g., "Claude Headless")
 * @param reason - Why authentication is missing (e.g., "No API key found")
 * @param actionHint - What the user should do (e.g., "Run `claude auth login`")
 * @param docsUrl - Optional URL to setup documentation
 */
export function throwAuthError(
  adapterName: string,
  reason: string,
  actionHint: string,
  docsUrl?: string
): never {
  let message = `${reason}\nFix: ${actionHint}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "AUTH_MISSING", adapterName, docsUrl);
}

/**
 * Throw a BINARY_NOT_FOUND error with installation instructions.
 *
 * @param adapterName - Human-readable adapter name
 * @param binaryName - The CLI binary that was not found (e.g., "claude")
 * @param installCmd - How to install the binary (e.g., "brew install claude")
 * @param docsUrl - Optional URL to setup documentation
 */
export function throwBinaryNotFound(
  adapterName: string,
  binaryName: string,
  installCmd: string,
  docsUrl?: string
): never {
  let message = `${binaryName} CLI is not installed or not in PATH.\nFix: ${installCmd}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "BINARY_NOT_FOUND", adapterName, docsUrl);
}

/**
 * Throw a MODEL_NOT_FOUND error with pull/load instructions.
 *
 * @param adapterName - Human-readable adapter name
 * @param modelName - The model that was not found
 * @param installCmd - How to download or load the model
 * @param serverCmd - Optional command to start the server
 * @param docsUrl - Optional URL to documentation
 */
export function throwModelNotFound(
  adapterName: string,
  modelName: string,
  installCmd: string,
  serverCmd?: string,
  docsUrl?: string
): never {
  let message = `Model '${modelName}' not found.\nFix: ${installCmd}`;
  if (serverCmd) message += `\nStart server: ${serverCmd}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "MODEL_NOT_FOUND", adapterName, docsUrl);
}

/**
 * Throw a SERVER_UNREACHABLE error with start instructions.
 *
 * @param adapterName - Human-readable adapter name
 * @param baseUrl - The URL that was not reachable
 * @param startCmd - How to start the local server
 * @param docsUrl - Optional URL to documentation
 */
export function throwServerUnreachable(
  adapterName: string,
  baseUrl: string,
  startCmd: string,
  docsUrl?: string
): never {
  let message = `Server not responding at ${baseUrl}.\nFix: Start the server with: ${startCmd}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "SERVER_UNREACHABLE", adapterName, docsUrl);
}

/**
 * Throw a VERSION_MISMATCH error with upgrade instructions.
 *
 * @param adapterName - Human-readable adapter name
 * @param currentVersion - The detected version
 * @param requiredVersion - The minimum required version
 * @param upgradeCmd - How to upgrade the binary
 * @param docsUrl - Optional URL to documentation
 */
export function throwVersionMismatch(
  adapterName: string,
  currentVersion: string,
  requiredVersion: string,
  upgradeCmd: string,
  docsUrl?: string
): never {
  let message = `Version mismatch: found ${currentVersion}, requires >=${requiredVersion}.\nFix: ${upgradeCmd}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "VERSION_MISMATCH", adapterName, docsUrl);
}

/**
 * Throw a CONFIG_INVALID error for missing or invalid configuration.
 *
 * @param adapterName - Human-readable adapter name
 * @param configKey - The environment variable or config key that is missing
 * @param details - What to set and where
 * @param docsUrl - Optional URL to documentation
 */
export function throwConfigInvalid(
  adapterName: string,
  configKey: string,
  details: string,
  docsUrl?: string
): never {
  let message = `${configKey} is required. ${details}`;
  if (docsUrl) message += `\nDocs: ${docsUrl}`;
  throw new AdapterError(message, "CONFIG_INVALID", adapterName, docsUrl);
}

/**
 * Throw a TIMEOUT error for commands that exceeded the time limit.
 *
 * @param adapterName - Human-readable adapter name
 * @param command - The command that timed out (e.g., "`claude auth status`")
 * @param timeoutMs - The timeout in milliseconds
 * @param manualHint - What the user should verify manually
 */
export function throwTimeoutError(
  adapterName: string,
  command: string,
  timeoutMs: number,
  manualHint: string
): never {
  const seconds = timeoutMs / 1000;
  const message = `${command} timed out after ${seconds}s. ${manualHint}`;
  throw new AdapterError(message, "TIMEOUT", adapterName);
}
