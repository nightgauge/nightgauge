/**
 * CI Check Helpers - Pure functions for CI check classification and retry logic
 *
 * These functions support the CI check gate and auto-fix retry loop in pr-merge.
 * Following the deterministic vs probabilistic architecture:
 * - CI waiting and classification: Deterministic (these pure functions)
 * - Auto-fix generation: Probabilistic (AI in skill)
 *
 * @see Issue #426 - CI check gate and auto-fix retry loop
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 */

/**
 * CI failure type classification
 */
export type CIFailureType =
  "lint" | "test" | "build" | "typecheck" | "security" | "format" | "unknown";

/**
 * Information about a failed CI check
 */
export interface CICheckFailure {
  /** Name of the check (e.g., "build", "test", "lint") */
  name: string;
  /** URL to view the check details */
  detailsUrl: string;
  /** Classified failure type */
  failureType: CIFailureType;
  /** Whether this is a transient failure that might succeed on retry */
  isTransient: boolean;
  /** Raw conclusion from GitHub (failure, timed_out, etc.) */
  conclusion: string;
}

/**
 * Result of CI check status evaluation
 */
export interface CICheckStatus {
  /** All checks have completed (no pending) */
  allComplete: boolean;
  /** All checks passed */
  allPassed: boolean;
  /** Number of pending checks */
  pendingCount: number;
  /** Number of failed checks */
  failedCount: number;
  /** Number of passed checks */
  passedCount: number;
  /** Failed check details */
  failures: CICheckFailure[];
  /** Whether any CI checks exist at all */
  hasChecks: boolean;
}

/**
 * Patterns for classifying CI failure types
 *
 * Uses case-insensitive matching against check names and error output.
 */
const FAILURE_TYPE_PATTERNS: Record<CIFailureType, RegExp[]> = {
  lint: [
    /\blint\b/i,
    /\beslint\b/i,
    /\bpylint\b/i,
    /\brubocop\b/i,
    /\bflake8\b/i,
    /\bstyle\b/i,
    /\bcode.?quality\b/i,
  ],
  test: [
    /\btest\b/i,
    /\bspec\b/i,
    /\bvitest\b/i,
    /\bjest\b/i,
    /\bpytest\b/i,
    /\brspec\b/i,
    /\bunit\b/i,
    /\be2e\b/i,
    /\bintegration\b/i,
    /\bcoverage\b/i,
  ],
  build: [
    /\bbuild\b/i,
    /\bcompile\b/i,
    /\bbundle\b/i,
    /\bwebpack\b/i,
    /\bvite\b/i,
    /\besbuild\b/i,
    /\brollup\b/i,
    /\bpackage\b/i,
  ],
  typecheck: [/\btype.?check\b/i, /\btsc\b/i, /\btypescript\b/i, /\btypes?\b/i, /\bmypy\b/i],
  security: [
    /\bsecurity\b/i,
    /\baudit\b/i,
    /\bcodeql\b/i,
    /\bsnyk\b/i,
    /\bdependabot\b/i,
    /\bvulnerability\b/i,
    /\bsast\b/i,
    /\bdast\b/i,
  ],
  format: [/\bformat\b/i, /\bprettier\b/i, /\bblack\b/i, /\bformatting\b/i],
  unknown: [],
};

/**
 * Patterns that indicate transient failures (might pass on retry)
 */
const TRANSIENT_FAILURE_PATTERNS: RegExp[] = [
  /timed?.?out/i,
  /\btimeout\b/i,
  /\bflaky\b/i,
  /network\s+error/i,
  /connection\s+refused/i,
  /econnreset/i,
  /enotfound/i,
  /rate\s*limit/i,
  /503\b/i,
  /502\b/i,
  /504\b/i,
  /\bretry\b/i,
];

/**
 * Classify a CI failure based on check name and optional error output
 *
 * @param checkName - Name of the CI check (e.g., "build", "test / unit")
 * @param errorOutput - Optional error output from the check
 * @returns Classified failure type
 *
 * @example
 * ```typescript
 * classifyCIFailure("build") // "build"
 * classifyCIFailure("test / vitest unit tests") // "test"
 * classifyCIFailure("lint (eslint)") // "lint"
 * classifyCIFailure("random-check") // "unknown"
 * ```
 */
export function classifyCIFailure(checkName: string, errorOutput?: string): CIFailureType {
  const combinedText = errorOutput ? `${checkName} ${errorOutput}` : checkName;

  // Check each failure type in priority order
  const typeOrder: CIFailureType[] = ["lint", "test", "build", "typecheck", "security", "format"];

  for (const type of typeOrder) {
    const patterns = FAILURE_TYPE_PATTERNS[type];
    for (const pattern of patterns) {
      if (pattern.test(combinedText)) {
        return type;
      }
    }
  }

  return "unknown";
}

/**
 * Check if a failure appears to be transient (might pass on retry)
 *
 * @param checkName - Name of the CI check
 * @param errorOutput - Optional error output from the check
 * @param conclusion - GitHub check conclusion (e.g., "timed_out", "failure")
 * @returns True if the failure appears transient
 *
 * @example
 * ```typescript
 * isTransientCIFailure("test", "", "timed_out") // true
 * isTransientCIFailure("test", "Network error", "failure") // true
 * isTransientCIFailure("test", "expect(x).toBe(y)", "failure") // false
 * ```
 */
export function isTransientCIFailure(
  checkName: string,
  errorOutput?: string,
  conclusion?: string
): boolean {
  // Timeout conclusion is always transient
  if (conclusion === "timed_out") {
    return true;
  }

  const combinedText = errorOutput ? `${checkName} ${errorOutput}` : checkName;

  // Check for transient patterns
  for (const pattern of TRANSIENT_FAILURE_PATTERNS) {
    if (pattern.test(combinedText)) {
      return true;
    }
  }

  return false;
}

/**
 * Parse CI check status from GitHub PR checks JSON
 *
 * Accepts the output of `gh pr checks --json` or `gh pr view --json statusCheckRollup`.
 *
 * @param checksJson - Array of check objects from GitHub CLI
 * @returns Parsed CI check status
 *
 * @example
 * ```typescript
 * const checks = [
 *   { name: "build", status: "COMPLETED", conclusion: "SUCCESS" },
 *   { name: "test", status: "COMPLETED", conclusion: "FAILURE", detailsUrl: "..." }
 * ];
 * const status = parseCICheckStatus(checks);
 * // { allComplete: true, allPassed: false, failedCount: 1, ... }
 * ```
 */
export function parseCICheckStatus(
  checksJson: Array<{
    name: string;
    status?: string;
    conclusion?: string;
    detailsUrl?: string;
  }>
): CICheckStatus {
  if (!checksJson || checksJson.length === 0) {
    return {
      allComplete: true,
      allPassed: true,
      pendingCount: 0,
      failedCount: 0,
      passedCount: 0,
      failures: [],
      hasChecks: false,
    };
  }

  let pendingCount = 0;
  let failedCount = 0;
  let passedCount = 0;
  const failures: CICheckFailure[] = [];

  for (const check of checksJson) {
    const status = (check.status || "").toUpperCase();
    const conclusion = (check.conclusion || "").toUpperCase();

    if (status !== "COMPLETED") {
      pendingCount++;
      continue;
    }

    if (conclusion === "SUCCESS" || conclusion === "NEUTRAL" || conclusion === "SKIPPED") {
      passedCount++;
    } else {
      failedCount++;
      failures.push({
        name: check.name,
        detailsUrl: check.detailsUrl || "",
        failureType: classifyCIFailure(check.name),
        isTransient: isTransientCIFailure(check.name, undefined, conclusion),
        conclusion: conclusion.toLowerCase(),
      });
    }
  }

  return {
    allComplete: pendingCount === 0,
    allPassed: failedCount === 0 && pendingCount === 0,
    pendingCount,
    failedCount,
    passedCount,
    failures,
    hasChecks: true,
  };
}

/**
 * Extract failed check information from raw gh pr checks output
 *
 * Parses the plain-text output of `gh pr checks` command.
 *
 * @param checksOutput - Raw output from `gh pr checks` command
 * @returns Array of failed check info
 *
 * @example
 * ```typescript
 * const output = `
 * build   pass   1m30s   https://github.com/.../1
 * test    fail   2m15s   https://github.com/.../2
 * lint    fail   0m45s   https://github.com/.../3
 * `;
 * const failures = extractFailedChecksFromOutput(output);
 * // [{ name: "test", detailsUrl: "...", ... }, { name: "lint", ... }]
 * ```
 */
export function extractFailedChecksFromOutput(checksOutput: string): CICheckFailure[] {
  const failures: CICheckFailure[] = [];
  const lines = checksOutput.trim().split("\n");

  for (const line of lines) {
    // Parse format: "check-name\tstatus\tduration\turl"
    // or "check-name  status  duration  url" (space-separated)
    const parts = line.split(/\t+|\s{2,}/);

    if (parts.length >= 2) {
      const name = parts[0].trim();
      const status = parts[1].trim().toLowerCase();
      const url = parts.length >= 4 ? parts[3].trim() : "";

      if (status === "fail" || status === "failure") {
        failures.push({
          name,
          detailsUrl: url,
          failureType: classifyCIFailure(name),
          isTransient: isTransientCIFailure(name),
          conclusion: "failure",
        });
      }
    }
  }

  return failures;
}

/**
 * Determine if CI check failures can potentially be auto-fixed
 *
 * Some failure types are more amenable to auto-fix than others.
 *
 * @param failures - Array of CI check failures
 * @returns True if at least one failure is auto-fixable
 *
 * @example
 * ```typescript
 * canAutoFixFailures([{ failureType: "lint", ... }]) // true
 * canAutoFixFailures([{ failureType: "security", ... }]) // false
 * canAutoFixFailures([{ failureType: "test", ... }]) // true
 * ```
 */
export function canAutoFixFailures(failures: CICheckFailure[]): boolean {
  // These failure types can potentially be auto-fixed
  const autoFixableTypes: CIFailureType[] = ["lint", "format", "typecheck", "build", "test"];

  return failures.some((f) => autoFixableTypes.includes(f.failureType));
}

/**
 * Get human-readable description of failure type
 *
 * @param failureType - CI failure type
 * @returns Human-readable description
 */
export function getFailureTypeDescription(failureType: CIFailureType): string {
  const descriptions: Record<CIFailureType, string> = {
    lint: "Linting error (code style/quality issue)",
    test: "Test failure (assertion or test error)",
    build: "Build failure (compilation or bundling error)",
    typecheck: "Type error (TypeScript or type checking failure)",
    security: "Security issue (vulnerability or audit failure)",
    format: "Formatting error (code formatting issue)",
    unknown: "Unknown failure type",
  };

  return descriptions[failureType];
}

/**
 * Prioritize failures for auto-fix attempts
 *
 * Returns failures sorted by auto-fix likelihood (highest first).
 * Format and lint issues are typically easiest to fix automatically.
 *
 * @param failures - Array of CI check failures
 * @returns Sorted array with most auto-fixable first
 */
export function prioritizeFailuresForAutoFix(failures: CICheckFailure[]): CICheckFailure[] {
  const priority: Record<CIFailureType, number> = {
    format: 1, // Highest - usually just run formatter
    lint: 2, // High - often has auto-fix
    typecheck: 3, // Medium - may need code changes
    build: 4, // Lower - often missing imports/deps
    test: 5, // Lower - needs understanding of test
    security: 6, // Low - often needs human review
    unknown: 7, // Lowest
  };

  return [...failures].sort((a, b) => priority[a.failureType] - priority[b.failureType]);
}

/**
 * Check if failures include repeat of same type (potential flaky test)
 *
 * Used to detect if we're hitting the same failure repeatedly,
 * which might indicate a flaky test or deeper issue.
 *
 * @param currentFailures - Current attempt's failures
 * @param previousFailures - Previous attempt's failures
 * @returns True if same failures are repeating
 */
export function hasRepeatingFailures(
  currentFailures: CICheckFailure[],
  previousFailures: CICheckFailure[]
): boolean {
  if (currentFailures.length === 0 || previousFailures.length === 0) {
    return false;
  }

  const currentNames = new Set(currentFailures.map((f) => f.name));
  const previousNames = new Set(previousFailures.map((f) => f.name));

  // Check if any check is failing in both attempts
  for (const name of currentNames) {
    if (previousNames.has(name)) {
      return true;
    }
  }

  return false;
}
