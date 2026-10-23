/**
 * Tests for retry helper functions
 */

import { describe, it, expect } from "vitest";
import {
  isRetryableApiError,
  extractApiErrorCode,
  calculateBackoffDelay,
  sanitizeApiError,
  isRateLimitError,
  DEFAULT_RETRY_CONFIG,
  type RetryConfig,
} from "../../src/utils/retryHelpers";

describe("retryHelpers", () => {
  describe("extractApiErrorCode", () => {
    it('should extract code from "API Error: 500" format', () => {
      expect(extractApiErrorCode("API Error: 500")).toBe(500);
    });

    it('should extract code from "Error: 502" format', () => {
      expect(extractApiErrorCode("Error: 502")).toBe(502);
    });

    it('should extract code from "500 Internal Server Error" format', () => {
      expect(extractApiErrorCode("500 Internal Server Error")).toBe(500);
    });

    it('should extract code from "HTTP 502" format', () => {
      expect(extractApiErrorCode("HTTP 502")).toBe(502);
    });

    it('should extract code from "503 Service Unavailable" format', () => {
      expect(extractApiErrorCode("503 Service Unavailable")).toBe(503);
    });

    it("should return null for non-API error messages", () => {
      expect(extractApiErrorCode("Validation error")).toBeNull();
      expect(extractApiErrorCode("Something went wrong")).toBeNull();
    });

    it("should return null for invalid status codes", () => {
      expect(extractApiErrorCode("Error: 999")).toBeNull();
      expect(extractApiErrorCode("Error: 0")).toBeNull();
    });
  });

  describe("isRetryableApiError", () => {
    const config: RetryConfig = DEFAULT_RETRY_CONFIG;

    it("should return true for HTTP 500 errors", () => {
      expect(isRetryableApiError("API Error: 500", config)).toBe(true);
    });

    it("should return true for HTTP 502 errors", () => {
      expect(isRetryableApiError("502 Bad Gateway", config)).toBe(true);
    });

    it("should return true for HTTP 503 errors", () => {
      expect(isRetryableApiError("503 Service Unavailable", config)).toBe(true);
    });

    it("should return true for HTTP 504 errors", () => {
      expect(isRetryableApiError("HTTP 504", config)).toBe(true);
    });

    it("should return false for HTTP 404 errors", () => {
      expect(isRetryableApiError("API Error: 404", config)).toBe(false);
    });

    it("should return false for HTTP 400 errors", () => {
      expect(isRetryableApiError("400 Bad Request", config)).toBe(false);
    });

    it("should return false for validation errors", () => {
      expect(isRetryableApiError("Validation error", config)).toBe(false);
    });

    it("should handle Error objects", () => {
      const error500 = new Error("API Error: 500");
      expect(isRetryableApiError(error500, config)).toBe(true);

      const error404 = new Error("API Error: 404");
      expect(isRetryableApiError(error404, config)).toBe(false);
    });

    it("should respect custom retryable_api_errors config", () => {
      const customConfig: RetryConfig = {
        ...DEFAULT_RETRY_CONFIG,
        retryable_api_errors: [500], // Only 500
      };

      expect(isRetryableApiError("API Error: 500", customConfig)).toBe(true);
      expect(isRetryableApiError("API Error: 502", customConfig)).toBe(false);
    });
  });

  describe("calculateBackoffDelay", () => {
    const config: RetryConfig = DEFAULT_RETRY_CONFIG;

    // Default schedule under #3619 retro: 5s, 10s, 20s, capped at 30s. The
    // previous 100ms initial-delay was effectively "no backoff" — Anthropic
    // 500 outages produced 4 immediate respawns and burned $2.41 in 25s.
    it("should calculate correct delay for attempt 0 (5s)", () => {
      expect(calculateBackoffDelay(0, config)).toBe(5000);
    });

    it("should calculate correct delay for attempt 1 (10s)", () => {
      expect(calculateBackoffDelay(1, config)).toBe(10000);
    });

    it("should calculate correct delay for attempt 2 (20s)", () => {
      expect(calculateBackoffDelay(2, config)).toBe(20000);
    });

    it("should cap delay at 30 seconds from attempt 3 onward", () => {
      // 5000 * 2^3 = 40000 → capped at 30000
      expect(calculateBackoffDelay(3, config)).toBe(30000);
      expect(calculateBackoffDelay(10, config)).toBe(30000);
      expect(calculateBackoffDelay(20, config)).toBe(30000);
    });

    it("should handle custom initial delay", () => {
      const customConfig: RetryConfig = {
        ...DEFAULT_RETRY_CONFIG,
        initial_delay_ms: 200,
      };
      expect(calculateBackoffDelay(0, customConfig)).toBe(200);
      expect(calculateBackoffDelay(1, customConfig)).toBe(400);
    });

    it("should handle custom backoff multiplier", () => {
      const customConfig: RetryConfig = {
        ...DEFAULT_RETRY_CONFIG,
        backoff_multiplier: 3,
        initial_delay_ms: 100,
      };
      expect(calculateBackoffDelay(0, customConfig)).toBe(100);
      expect(calculateBackoffDelay(1, customConfig)).toBe(300);
      expect(calculateBackoffDelay(2, customConfig)).toBe(900);
    });
  });

  describe("sanitizeApiError", () => {
    it("should redact file paths with /Users/", () => {
      const error = "Error at /Users/foo/repo/file.ts";
      expect(sanitizeApiError(error)).toBe("Error at [path redacted]");
    });

    it("should redact file paths with /home/", () => {
      const error = "Error at /home/bar/repo/file.ts";
      expect(sanitizeApiError(error)).toBe("Error at [path redacted]");
    });

    it("should redact Windows paths", () => {
      const error = "Error at C:\\Users\\foo\\repo\\file.ts";
      expect(sanitizeApiError(error)).toBe("Error at [path redacted]");
    });

    it("should redact API keys with sk- prefix", () => {
      const error = "Token: sk-abc123";
      expect(sanitizeApiError(error)).toBe("Token: [redacted]");
    });

    it("should redact GitHub tokens with ghp_ prefix", () => {
      const error = "Token: ghp_abc123";
      expect(sanitizeApiError(error)).toBe("Token: [redacted]");
    });

    it("should redact Bearer tokens", () => {
      const error = "Authorization: Bearer abc123xyz";
      expect(sanitizeApiError(error)).toBe("Authorization: Bearer [redacted]");
    });

    it("should redact query parameters with secrets", () => {
      const error = "URL: https://api.example.com?token=abc123";
      expect(sanitizeApiError(error)).toContain("?token=[redacted]");
    });

    it("should remove stack traces", () => {
      const error = "Error: Something failed\n    at foo (file.ts:10:5)\n    at bar (file.ts:20:3)";
      const sanitized = sanitizeApiError(error);
      expect(sanitized).not.toContain("at foo");
      expect(sanitized).not.toContain("file.ts:10:5");
    });

    it("should preserve HTTP status codes", () => {
      const error = "API Error: 500 Internal Server Error";
      expect(sanitizeApiError(error)).toBe("API Error: 500 Internal Server Error");
    });

    it("should preserve generic error descriptions", () => {
      const error = "Connection timeout";
      expect(sanitizeApiError(error)).toBe("Connection timeout");
    });

    it("should handle multiple sensitive data types in one string", () => {
      const error = "Error at /Users/foo/repo with token sk-abc123 at file:10:5";
      const sanitized = sanitizeApiError(error);
      expect(sanitized).toContain("[path redacted]");
      expect(sanitized).toContain("[redacted]");
      expect(sanitized).not.toContain("sk-abc123");
    });
  });

  describe("isRateLimitError", () => {
    it("should return true for HTTP 429 errors", () => {
      expect(isRateLimitError("API Error: 429")).toBe(true);
    });

    it('should return true for "429 Too Many Requests"', () => {
      expect(isRateLimitError("429 Too Many Requests")).toBe(true);
    });

    it("should return false for HTTP 500 errors", () => {
      expect(isRateLimitError("API Error: 500")).toBe(false);
    });

    it("should return false for non-API errors", () => {
      expect(isRateLimitError("Validation error")).toBe(false);
    });

    it("should handle Error objects", () => {
      const error429 = new Error("API Error: 429");
      expect(isRateLimitError(error429)).toBe(true);

      const error500 = new Error("API Error: 500");
      expect(isRateLimitError(error500)).toBe(false);
    });
  });
});
