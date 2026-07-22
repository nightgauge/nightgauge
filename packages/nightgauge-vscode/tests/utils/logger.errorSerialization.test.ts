/**
 * logger.errorSerialization.test.ts
 *
 * Verifies that Logger.warn() (and all log methods using formatMessage) correctly
 * serializes Error instances in the data argument. Before this fix, Error objects
 * were passed through JSON.stringify which produces "{}" for non-enumerable
 * properties like message and stack.
 *
 * @see Issue #1491 - Batch progress/result persistence always fails with empty error
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { Logger } from "../../src/utils/logger";

// Mock vscode module
vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

import * as vscode from "vscode";

describe("Logger — Error serialization in formatMessage", () => {
  let logger: Logger;
  let mockAppendLine: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mockAppendLine = vi.fn();
    vi.mocked(vscode.window.createOutputChannel).mockReturnValue({
      appendLine: mockAppendLine,
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.OutputChannel);
    logger = new Logger("Test");
  });

  it("should serialize Error.message in warn() data", () => {
    const err = new Error("boom");
    logger.warn("Something failed", { err });

    expect(mockAppendLine).toHaveBeenCalledOnce();
    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("boom");
    expect(logged).not.toContain('"err":{}');
  });

  it("should include error name and stack in warn() output", () => {
    const err = new TypeError("type mismatch");
    logger.warn("Type error occurred", { err });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("type mismatch");
    expect(logged).toContain("TypeError");
    expect(logged).toContain("stack");
  });

  it("should handle nested Error in warn() data", () => {
    const inner = new Error("nested");
    logger.warn("Nested error", { data: { err: inner } });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("nested");
  });

  it("should handle mixed data with Error and primitives in warn()", () => {
    const err = new Error("mixed");
    logger.warn("Mixed data", { code: 42, err, str: "ok" });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("mixed");
    expect(logged).toContain('"code":42');
    expect(logged).toContain('"str":"ok"');
    expect(logged).not.toContain('"err":{}');
  });

  it("should not affect non-Error objects in warn() data", () => {
    logger.warn("Plain data", { count: 5, label: "test" });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain('"count":5');
    expect(logged).toContain('"label":"test"');
  });

  it("should serialize Error in debug() data", () => {
    const err = new Error("debug error");
    logger.debug("Debug event", { err });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("debug error");
    expect(logged).not.toContain('"err":{}');
  });

  it("should serialize Error in info() data", () => {
    const err = new Error("info error");
    logger.info("Info event", { err });

    const logged = mockAppendLine.mock.calls[0][0] as string;
    expect(logged).toContain("info error");
    expect(logged).not.toContain('"err":{}');
  });
});
