const POSITIVE_INTEGER_PATTERN = /^[1-9]\d*$/;

/**
 * Parse a user-entered GitHub issue number without accepting numeric prefixes.
 */
export function parsePositiveIssueNumber(value: string): number | null {
  if (!POSITIVE_INTEGER_PATTERN.test(value)) {
    return null;
  }

  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

export function validatePositiveIssueNumber(value: string): string | null {
  return parsePositiveIssueNumber(value) === null
    ? "Please enter a valid positive issue number"
    : null;
}
