/**
 * RecommendationApplier.test.ts
 *
 * Unit tests for RecommendationApplier service, focusing on:
 * - Applying config patches via IncrediYamlService
 * - Error handling when writes fail
 * - 30-second revert window management
 * - Applied categories tracking
 * - Dispose cleanup
 *
 * @see Issue #787 - Actionable Dashboard Recommendations
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode module (required because IncrediYamlService imports it)
vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [] },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  Uri: { file: (path: string) => ({ fsPath: path }) },
  Disposable: { from: vi.fn() },
}));

// Mock IncrediYamlService
const mockRead = vi.fn();
const mockWrite = vi.fn();
const mockDispose = vi.fn();

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return { read: mockRead, write: mockWrite, dispose: mockDispose };
  }),
}));

import { RecommendationApplier } from "../../src/services/RecommendationApplier";

describe("RecommendationApplier", () => {
  let applier: RecommendationApplier;

  beforeEach(() => {
    vi.clearAllMocks();
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });
    applier = new RecommendationApplier("/workspace");
  });

  afterEach(() => {
    applier.dispose();
    vi.useRealTimers();
  });

  it("apply() writes config with merged patch", async () => {
    // Arrange
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });

    // Act
    const result = await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Assert
    expect(mockWrite).toHaveBeenCalledWith(
      expect.objectContaining({
        pipeline: expect.objectContaining({ max_turns: 5 }),
      }),
      "project"
    );
    expect(result).toEqual({ success: true, previousValue: 10 });
  });

  it("apply() returns error when write fails", async () => {
    // Arrange
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({
      success: false,
      error: "Permission denied",
    });

    // Act
    const result = await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Assert
    expect(result).toEqual({
      success: false,
      error: "Permission denied",
    });
  });

  it("revert() restores previous value within 30s window", async () => {
    // Arrange
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });
    await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Reset to track revert write call specifically
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 5 } },
    });
    mockWrite.mockClear();
    mockWrite.mockResolvedValue({ success: true });

    // Act
    const result = await applier.revert("oversized-context");

    // Assert
    expect(result).toEqual({ success: true });
    expect(mockWrite).toHaveBeenCalledWith(
      expect.objectContaining({
        pipeline: expect.objectContaining({ max_turns: 10 }),
      }),
      "project"
    );
  });

  it("revert() fails after window expires", async () => {
    // Arrange
    vi.useFakeTimers();
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });
    await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Act
    vi.advanceTimersByTime(31_000);
    const result = await applier.revert("oversized-context");

    // Assert
    expect(result).toEqual({
      success: false,
      error: "No revert state available (window may have expired)",
    });
  });

  it("getAppliedCategories() returns applied categories", async () => {
    // Arrange
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });

    // Act
    await applier.apply("oversized-context", "pipeline.max_turns", 5);
    await applier.apply("slow-validation", "pipeline.timeout", 60);
    const categories = applier.getAppliedCategories();

    // Assert
    expect(categories).toContain("oversized-context");
    expect(categories).toContain("slow-validation");
    expect(categories).toHaveLength(2);
  });

  it("canRevert() returns true during window, false after", async () => {
    // Arrange
    vi.useFakeTimers();
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });
    await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Act & Assert - within window
    expect(applier.canRevert("oversized-context")).toBe(true);

    // Act & Assert - after window expires
    vi.advanceTimersByTime(31_000);
    expect(applier.canRevert("oversized-context")).toBe(false);
  });

  it("dispose() clears all revert timers", async () => {
    // Arrange
    vi.useFakeTimers();
    mockRead.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWrite.mockResolvedValue({ success: true });
    await applier.apply("oversized-context", "pipeline.max_turns", 5);
    await applier.apply("slow-validation", "pipeline.timeout", 60);

    // Act
    applier.dispose();

    // Assert - advancing timers should not cause errors
    vi.advanceTimersByTime(31_000);
    expect(applier.canRevert("oversized-context")).toBe(false);
    expect(applier.canRevert("slow-validation")).toBe(false);
    expect(mockDispose).toHaveBeenCalled();
  });
});
