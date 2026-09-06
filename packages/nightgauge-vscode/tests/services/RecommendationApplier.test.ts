/**
 * RecommendationApplier.test.ts
 *
 * Unit tests for RecommendationApplier service, focusing on:
 * - Applying config patches via NightgaugeYamlService
 * - Routing the write to the runtime tier, never the committed team config
 * - Error handling when writes fail
 * - 30-second revert window management
 * - Applied categories tracking
 * - Dispose cleanup
 *
 * @see Issue #787 - Actionable Dashboard Recommendations
 * @see Issue #1516 - applying a recommendation is a runtime write, so it goes
 *      to the local (or machine) tier, not to `.nightgauge/config.yaml`
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode module (required because NightgaugeYamlService imports it)
vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [] },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  Uri: { file: (path: string) => ({ fsPath: path }) },
  Disposable: { from: vi.fn() },
}));

// Mock NightgaugeYamlService
const mockReadMerged = vi.fn();
const mockWriteRuntimeValue = vi.fn();
const mockWrite = vi.fn();
const mockDispose = vi.fn();

vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: vi.fn(function () {
    return {
      readMerged: mockReadMerged,
      writeRuntimeValue: mockWriteRuntimeValue,
      write: mockWrite,
      dispose: mockDispose,
    };
  }),
}));

import { RecommendationApplier } from "../../src/services/RecommendationApplier";

describe("RecommendationApplier", () => {
  let applier: RecommendationApplier;

  beforeEach(() => {
    vi.clearAllMocks();
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });
    applier = new RecommendationApplier("/workspace");
  });

  afterEach(() => {
    applier.dispose();
    vi.useRealTimers();
  });

  it("apply() writes the single key through the runtime-tier router", async () => {
    // Arrange
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });

    // Act
    const result = await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Assert
    expect(mockWriteRuntimeValue).toHaveBeenCalledWith("pipeline.max_turns", 5);
    expect(result).toEqual({ success: true, previousValue: 10 });
  });

  // #1516: the committed team config is not a target for this flow at all.
  it("apply() never writes the project tier", async () => {
    await applier.apply("oversized-context", "pipeline.max_turns", 5);

    expect(mockWrite).not.toHaveBeenCalled();
  });

  it("apply() returns error when write fails", async () => {
    // Arrange
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({
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

  it.each(["__proto__.polluted", "constructor.prototype.polluted", "prototype.polluted"])(
    "apply() rejects unsafe path %s without writing",
    async (path) => {
      const result = await applier.apply("unsafe", path, true);

      expect(result).toEqual({ success: false, error: "Unsafe configuration path" });
      expect(mockWriteRuntimeValue).not.toHaveBeenCalled();
    }
  );

  it("revert() restores previous value within 30s window", async () => {
    // Arrange
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });
    await applier.apply("oversized-context", "pipeline.max_turns", 5);

    // Reset to track the revert write call specifically
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 5 } },
    });
    mockWriteRuntimeValue.mockClear();
    mockWriteRuntimeValue.mockResolvedValue({ success: true });

    // Act
    const result = await applier.revert("oversized-context");

    // Assert
    expect(result).toEqual({ success: true });
    expect(mockWriteRuntimeValue).toHaveBeenCalledWith("pipeline.max_turns", 10);
    expect(mockWrite).not.toHaveBeenCalled();
  });

  it("revert() fails after window expires", async () => {
    // Arrange
    vi.useFakeTimers();
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });
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
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });

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
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });
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
    mockReadMerged.mockResolvedValue({
      config: { pipeline: { max_turns: 10 } },
    });
    mockWriteRuntimeValue.mockResolvedValue({ success: true });
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
