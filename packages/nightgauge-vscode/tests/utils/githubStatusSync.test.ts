/**
 * Unit tests for githubStatusSync utility
 *
 * Tests the resetGitHubStatus function which delegates to
 * projectFieldWriter.updateProjectItemStatus for direct project
 * board field updates (no label manipulation).
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { resetGitHubStatus } from "../../src/utils/githubStatusSync";
import type { Logger } from "../../src/utils/logger";

// ============================================================================
// Mocks
// ============================================================================

vi.mock("../../src/utils/projectFieldWriter", () => ({
  updateProjectItemStatus: vi.fn(),
}));

import { updateProjectItemStatus } from "../../src/utils/projectFieldWriter";

// ============================================================================
// Test Fixtures
// ============================================================================

const MOCK_CWD = "/test/workspace";

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

// ============================================================================
// Tests
// ============================================================================

describe("githubStatusSync", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();
  });

  describe("input validation", () => {
    it("should reject negative issue numbers", async () => {
      const result = await resetGitHubStatus(-1, MOCK_CWD, logger);
      expect(result.success).toBe(false);
      expect(result.error).toContain("Invalid issue number");
    });

    it("should reject zero", async () => {
      const result = await resetGitHubStatus(0, MOCK_CWD, logger);
      expect(result.success).toBe(false);
      expect(result.error).toContain("Invalid issue number");
    });

    it("should reject non-integer numbers", async () => {
      const result = await resetGitHubStatus(1.5, MOCK_CWD, logger);
      expect(result.success).toBe(false);
      expect(result.error).toContain("Invalid issue number");
    });

    it("should not call updateProjectItemStatus for invalid input", async () => {
      await resetGitHubStatus(-1, MOCK_CWD, logger);
      expect(updateProjectItemStatus).not.toHaveBeenCalled();
    });
  });

  describe("successful status reset", () => {
    it("should delegate to updateProjectItemStatus with Ready status", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      const result = await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(result.success).toBe(true);
      expect(updateProjectItemStatus).toHaveBeenCalledWith(42, "Ready", MOCK_CWD, logger);
    });

    it("should log success message", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(logger.info).toHaveBeenCalledWith(
        "GitHub status reset to Ready",
        expect.objectContaining({ issueNumber: 42 })
      );
    });
  });

  describe("failure handling", () => {
    it("should return failure when updateProjectItemStatus fails", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({
        success: false,
        error: "No project configuration found",
      });

      const result = await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(result.success).toBe(false);
      expect(result.error).toBe("No project configuration found");
    });

    it("should log warning on failure", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({
        success: false,
        error: "API error",
      });

      await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(logger.warn).toHaveBeenCalledWith(
        "Failed to reset GitHub status",
        expect.objectContaining({ issueNumber: 42 })
      );
    });

    it("should handle thrown errors gracefully", async () => {
      vi.mocked(updateProjectItemStatus).mockRejectedValue(new Error("Network error"));

      const result = await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(result.success).toBe(false);
      expect(result.error).toContain("Network error");
    });

    it("should log warning for thrown errors", async () => {
      vi.mocked(updateProjectItemStatus).mockRejectedValue(new Error("Network error"));

      await resetGitHubStatus(42, MOCK_CWD, logger);

      expect(logger.warn).toHaveBeenCalledWith(
        "Failed to reset GitHub status (will continue with local cleanup)",
        expect.objectContaining({ issueNumber: 42 })
      );
    });
  });
});
