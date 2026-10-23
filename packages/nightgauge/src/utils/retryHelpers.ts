/**
 * Retry helpers for API error handling with exponential backoff
 *
 * Pure functions for retry logic to enable easy testing without mocking.
 * These functions implement automatic retry with exponential backoff for
 * transient API errors (500, 502, 503, 504) in the pipeline.
 *
 * @see Issue #79 - API error retry with exponential backoff
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 */

/**
 * Configuration for retry behavior
 */
export interface RetryConfig {
  /** Maximum number of automatic retry attempts (default: 3) */
  max_auto_attempts: number;
  /** Backoff multiplier for exponential delay (default: 2) */
  backoff_multiplier: number;
  /** Initial delay in milliseconds (default: 5000) */
  initial_delay_ms: number;
  /** HTTP status codes that trigger automatic retry (default: [500, 502, 503, 504]) */
  retryable_api_errors: number[];
  /** Delay for rate limit errors in milliseconds (default: 60000) */
  rate_limit_delay_ms: number;
}

/**
 * Default retry configuration
 *
 * `initial_delay_ms` was 100ms historically — issue #3619 retro showed that
 * was effectively "respawn claude immediately." Anthropic 500 errors on
 * issue #3340 produced 4 attempts within ~25 seconds, each spawning a fresh
 * 19K-token prompt, burning $2.41 on a transient outage that resolved within
 * the same window. 5s gives the upstream API enough time to recover from a
 * short blip; combined with the 2× multiplier the schedule becomes 5s, 10s,
 * 20s (capped at 30s by `calculateBackoffDelay`) for a total ~35s spread —
 * meaningful pause without forcing operators to wait minutes on every flake.
 */
export const DEFAULT_RETRY_CONFIG: RetryConfig = {
  max_auto_attempts: 3,
  backoff_multiplier: 2,
  initial_delay_ms: 5000,
  retryable_api_errors: [500, 502, 503, 504],
  rate_limit_delay_ms: 60000,
};

/**
 * Check if an error is a retryable API error
 *
 * Detects HTTP 5xx errors that indicate transient API failures.
 * Does NOT retry client errors (4xx) or validation errors.
 *
 * @param error - Error message or Error object
 * @param config - Retry configuration
 * @returns true if error is retryable
 *
 * @example
 * ```typescript
 * isRetryableApiError("API Error: 500") // true
 * isRetryableApiError("API Error: 404") // false
 * isRetryableApiError("Validation error") // false
 * isRetryableApiError("502 Bad Gateway") // true
 * ```
 */
export function isRetryableApiError(
  error: string | Error,
  config: RetryConfig = DEFAULT_RETRY_CONFIG
): boolean {
  const errorMessage = typeof error === "string" ? error : error.message || String(error);

  // Extract HTTP status code from error message
  const statusCode = extractApiErrorCode(errorMessage);
  if (statusCode === null) {
    return false;
  }

  // Check if status code is in the retryable list
  return config.retryable_api_errors.includes(statusCode);
}

/**
 * Extract HTTP status code from error message
 *
 * Handles various error message formats:
 * - "API Error: 500"
 * - "500 Internal Server Error"
 * - "HTTP 502"
 * - "Error 503: Service Unavailable"
 *
 * @param errorMessage - Error message string
 * @returns HTTP status code or null if not found
 *
 * @example
 * ```typescript
 * extractApiErrorCode("API Error: 500") // 500
 * extractApiErrorCode("502 Bad Gateway") // 502
 * extractApiErrorCode("Validation error") // null
 * ```
 */
export function extractApiErrorCode(errorMessage: string): number | null {
  // Pattern 1: "API Error: 500" or "Error: 500"
  const apiErrorPattern = /(?:API )?Error:?\s*(\d{3})/i;
  const apiMatch = errorMessage.match(apiErrorPattern);
  if (apiMatch) {
    const code = Number.parseInt(apiMatch[1], 10);
    // Only return if it's a valid HTTP status code range
    if (code >= 100 && code < 600) {
      return code;
    }
    return null;
  }

  // Pattern 2: "500 Internal Server Error" or "HTTP 502"
  const statusFirstPattern = /(?:HTTP\s+)?(\d{3})/i;
  const statusMatch = errorMessage.match(statusFirstPattern);
  if (statusMatch) {
    const code = Number.parseInt(statusMatch[1], 10);
    // Only return if it's a valid HTTP status code range
    if (code >= 100 && code < 600) {
      return code;
    }
  }

  return null;
}

/**
 * Calculate exponential backoff delay for retry attempt
 *
 * Formula: min(initial_delay * (backoff_multiplier ^ attempt), 30000)
 *
 * Examples with default config (initial=5000ms, multiplier=2):
 * - Attempt 0: 5000ms
 * - Attempt 1: 10000ms
 * - Attempt 2: 20000ms
 * - Attempt 3+: 30000ms (capped)
 *
 * @param attempt - Retry attempt number (0-indexed)
 * @param config - Retry configuration
 * @returns Delay in milliseconds
 *
 * @example
 * ```typescript
 * calculateBackoffDelay(0, config) // 5000ms
 * calculateBackoffDelay(1, config) // 10000ms
 * calculateBackoffDelay(2, config) // 20000ms
 * calculateBackoffDelay(3, config) // 30000ms (capped)
 * ```
 */
export function calculateBackoffDelay(
  attempt: number,
  config: RetryConfig = DEFAULT_RETRY_CONFIG
): number {
  const MAX_DELAY_MS = 30000; // Hard cap at 30 seconds

  const exponentialDelay = config.initial_delay_ms * config.backoff_multiplier ** attempt;

  return Math.min(exponentialDelay, MAX_DELAY_MS);
}

/**
 * Sanitize API error message for user display
 *
 * Removes sensitive information:
 * - File system paths with usernames
 * - API keys and tokens
 * - Internal stack traces
 * - Query parameters with auth tokens
 *
 * Keeps:
 * - HTTP status codes
 * - Generic error descriptions
 * - Service names
 *
 * @param error - Raw error message
 * @returns Sanitized error message
 *
 * @example
 * ```typescript
 * sanitizeApiError("Error at /Users/foo/repo")
 * // "Error at [path redacted]"
 *
 * sanitizeApiError("Token: sk-abc123")
 * // "Token: [redacted]"
 *
 * sanitizeApiError("API Error: 500 Internal Server Error")
 * // "API Error: 500 Internal Server Error"
 * ```
 */
export function sanitizeApiError(error: string): string {
  let sanitized = error;

  // Redact file system paths (absolute paths with /Users/, /home/, C:\)
  sanitized = sanitized.replace(/\/Users\/[^\s/]+[^\s]*/g, "[path redacted]");
  sanitized = sanitized.replace(/\/home\/[^\s/]+[^\s]*/g, "[path redacted]");
  sanitized = sanitized.replace(/C:\\[^\s]*/g, "[path redacted]");

  // Redact API keys and tokens (patterns like sk-..., ghp_..., Bearer ...)
  sanitized = sanitized.replace(/\b(?:sk|ghp|gho|ghu|ghs|ghr)[_-][a-zA-Z0-9_-]+/g, "[redacted]");
  sanitized = sanitized.replace(/Bearer\s+[a-zA-Z0-9_-]+/gi, "Bearer [redacted]");

  // Redact query parameters with sensitive data (?token=..., ?key=...)
  sanitized = sanitized.replace(/[?&](token|key|password|secret)=[^&\s]*/gi, "?$1=[redacted]");

  // Redact stack traces (lines starting with "at ")
  const lines = sanitized.split("\n");
  const filteredLines = lines.filter((line) => {
    // Remove stack trace lines (e.g., "    at foo (file.ts:10:5)")
    const trimmed = line.trim();
    if (trimmed.startsWith("at ") && /\([^)]+:\d+:\d+\)/.test(trimmed)) {
      return false;
    }
    return true;
  });
  sanitized = filteredLines.join("\n");

  return sanitized.trim();
}

/**
 * Check if an error is a rate limit error
 *
 * Detects HTTP 429 (Too Many Requests) status code.
 *
 * @param error - Error message or Error object
 * @returns true if error is a rate limit error
 *
 * @example
 * ```typescript
 * isRateLimitError("API Error: 429") // true
 * isRateLimitError("429 Too Many Requests") // true
 * isRateLimitError("API Error: 500") // false
 * ```
 */
export function isRateLimitError(error: string | Error): boolean {
  const errorMessage = typeof error === "string" ? error : error.message || String(error);

  const statusCode = extractApiErrorCode(errorMessage);
  return statusCode === 429;
}
