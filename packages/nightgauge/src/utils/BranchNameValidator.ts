/**
 * BranchNameValidator - Input validation for git branch names
 *
 * Prevents shell injection attacks by validating branch names against an
 * allowlist of safe characters before they are used in shell commands.
 *
 * @see Issue #2491 - Fix shell injection via unsanitized branch names
 */

/**
 * Allowlist pattern for safe git branch name characters.
 *
 * Permitted: alphanumeric, hyphen, underscore, forward slash, dot
 * This matches the safe subset of git-check-ref-format(1) rules.
 */
const BRANCH_NAME_ALLOWLIST = /^[a-zA-Z0-9\-_/.]+$/;

/**
 * Maximum allowed branch name length.
 * Git itself supports up to ~4096 bytes but long names are impractical.
 */
const MAX_BRANCH_NAME_LENGTH = 250;

/**
 * Patterns that are dangerous even within the allowlist — reject these
 * explicitly to block git ref tricks:
 *   @{    — git reflog notation (e.g. @{-1} checks out previous branch)
 *   ..    — range notation (e.g. main..feature) and path traversal
 *   //    — double slash (no valid use in branch names)
 *   /.    — segment starting with dot (git forbids these)
 *   .lock — git lock files
 */
const DISALLOWED_PATTERNS: RegExp[] = [
  /@\{/, // @{ reflog syntax
  /\.\./, // range/traversal
  /\/\//, // double slash
  /\/\./, // segment starting with dot
  /\.lock$/, // git lock file suffix
  /^-/, // starts with dash (interpreted as flag)
  /\/$/, // ends with slash
  /\.$/, // ends with dot
];

export interface BranchValidationResult {
  valid: boolean;
  reason?: string;
}

/**
 * Validate a git branch name to prevent shell injection attacks.
 *
 * Uses an allowlist approach: only characters known to be safe for git branch
 * names are permitted. Rejects shell metacharacters ($, `, ;, &, |, >, <, !,
 * spaces, etc.) that could be exploited when the branch name is interpolated
 * into a shell command string.
 *
 * @param branchName - The branch name to validate
 * @returns Validation result with reason on failure
 *
 * @example
 * validateBranchName("feat/42-dark-mode") // { valid: true }
 * validateBranchName("feat/$(whoami)")     // { valid: false, reason: "..." }
 */
export function validateBranchName(branchName: string): BranchValidationResult {
  if (!branchName || branchName.length === 0) {
    return { valid: false, reason: "Branch name must not be empty" };
  }

  if (branchName.length > MAX_BRANCH_NAME_LENGTH) {
    return {
      valid: false,
      reason: `Branch name exceeds maximum length of ${MAX_BRANCH_NAME_LENGTH} characters`,
    };
  }

  if (!BRANCH_NAME_ALLOWLIST.test(branchName)) {
    // Identify the first offending character for a clearer error message
    const offending = branchName.split("").find((ch) => !/[a-zA-Z0-9\-_/.]/.test(ch));
    return {
      valid: false,
      reason: `Branch name contains disallowed character: ${JSON.stringify(offending)}. Only alphanumeric characters, hyphens, underscores, forward slashes, and dots are permitted.`,
    };
  }

  for (const pattern of DISALLOWED_PATTERNS) {
    if (pattern.test(branchName)) {
      return {
        valid: false,
        reason: `Branch name contains disallowed pattern matching ${pattern}: "${branchName}"`,
      };
    }
  }

  return { valid: true };
}

/**
 * Assert that a branch name is valid, throwing an error if not.
 *
 * Use this before passing a branch name to any shell command or git operation.
 *
 * @param branchName - The branch name to validate
 * @param context - Optional context for the error message (e.g., "baseBranch")
 * @throws Error if the branch name is invalid
 */
export function assertValidBranchName(branchName: string, context?: string): void {
  const result = validateBranchName(branchName);
  if (!result.valid) {
    const ctx = context ? ` (${context})` : "";
    throw new Error(`Invalid branch name${ctx}: ${result.reason}`);
  }
}
