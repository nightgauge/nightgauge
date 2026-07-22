/**
 * Shared CLI auth validation - Consolidates adapter auth checking.
 *
 * Replaces the near-identical validateClaudeHeadlessAuth() and
 * validateCodexAuth() functions with a single shared implementation.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #2596 - Standardize adapter error messages
 */

import { type PreflightCommandRunner, type PreflightCommandResult } from "../codexPreflight.js";
import { AdapterError, type AdapterErrorCategory, throwBinaryNotFound } from "./errors.js";

/**
 * An auth subcommand to try (e.g., { args: ['auth', 'status'] }).
 * The first one that returns exit code 0 is accepted.
 */
export interface AuthSubcommand {
  args: string[];
}

/**
 * Validate CLI authentication by trying a sequence of auth subcommands.
 *
 * @param command - The CLI binary name (e.g., 'claude', 'codex')
 * @param authSubcommands - Ordered list of subcommands to try
 * @param runner - Command runner (injected for testing)
 * @param cwd - Working directory
 * @param adapterName - Human-readable adapter name (e.g., 'Claude Headless'), used in AdapterError
 * @param errorCategory - Error category; defaults to AUTH_MISSING
 * @param loginHint - The command to run to authenticate (e.g., 'claude auth login')
 * @param docsUrl - Optional URL to authentication documentation
 * @returns 'passed' if any subcommand succeeds
 * @throws AdapterError if all subcommands fail
 */
export async function validateCLIAuth(options: {
  command: string;
  authSubcommands: AuthSubcommand[];
  runner: PreflightCommandRunner;
  cwd: string;
  adapterName: string;
  errorCategory?: AdapterErrorCategory;
  loginHint: string;
  docsUrl?: string;
}): Promise<"passed"> {
  let lastAuthError = "";

  for (const sub of options.authSubcommands) {
    const result = await options.runner(options.command, sub.args, options.cwd);
    if (result.code === 0) {
      return "passed";
    }

    const combined = `${result.stderr}\n${result.stdout}`.trim();
    if (combined.length > 0) {
      lastAuthError = combined;
    }
  }

  const category = options.errorCategory ?? "AUTH_MISSING";
  let message =
    `${options.command} CLI is not authenticated. ` +
    `Run \`${options.loginHint}\` to authenticate.`;
  if (lastAuthError) message += `\nDetails: ${lastAuthError}`;
  if (options.docsUrl) message += `\nDocs: ${options.docsUrl}`;

  throw new AdapterError(message, category, options.adapterName, options.docsUrl);
}

/**
 * Verify that a CLI binary is installed and available in PATH.
 *
 * When `adapterName` and `installCmd` are provided, throws `AdapterError`
 * with BINARY_NOT_FOUND category and installation instructions.
 * Otherwise throws `CodexPreflightError` for backward compatibility.
 *
 * @param command - CLI binary to check (e.g., 'claude', 'codex')
 * @param runner - Command runner (injected for testing)
 * @param cwd - Working directory
 * @param adapterName - Human-readable adapter name for AdapterError
 * @param installCmd - How to install the binary (e.g., 'brew install claude')
 * @param docsUrl - Optional URL to setup documentation
 */
export async function verifyCLIInstalled(options: {
  command: string;
  runner: PreflightCommandRunner;
  cwd: string;
  adapterName: string;
  installCmd: string;
  docsUrl?: string;
}): Promise<PreflightCommandResult> {
  let result: PreflightCommandResult;
  try {
    result = await options.runner(options.command, ["--version"], options.cwd);
  } catch {
    throwBinaryNotFound(options.adapterName, options.command, options.installCmd, options.docsUrl);
  }

  if (result!.code !== 0) {
    throwBinaryNotFound(options.adapterName, options.command, options.installCmd, options.docsUrl);
  }

  return result!;
}

// Re-export AdapterError as the primary error type for adapter consumers
export { AdapterError };
