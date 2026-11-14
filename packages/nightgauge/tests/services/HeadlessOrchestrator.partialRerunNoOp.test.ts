/**
 * #3163 AC 4: a zero-change feature-dev on a partial-delivery re-run is booked
 * `not_pipeline_actionable` — a no-op, never a failure — so it feeds neither
 * the lifetime cap nor the cascade breaker. Mirrors the Go scheduler's
 * decidePartialNoOp booking.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
    getConfiguration: vi.fn(() => ({ get: vi.fn(() => undefined) })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  commands: { executeCommand: vi.fn(), registerCommand: vi.fn() },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  ExtensionMode: { Production: 1, Development: 2, Test: 3 },
}));

import { classifyTerminalKind } from "@nightgauge/sdk";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";

const noOp = {
  error: new Error(
    "premature turn end: stage exited 0 with no state change (gate no-op): dev context records zero file changes"
  ),
  noOp: true,
};

describe("HeadlessOrchestrator.partialRerunNoOpBooking (#3163)", () => {
  it("books a scoped feature-dev no-op as not_pipeline_actionable", () => {
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(1750, "feature-dev", noOp, true);
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.terminalKind).toBe("not_pipeline_actionable");
    // The run-record ladder must read the same kind from the error text, or
    // the lifetime cap would still see premature_turn_end.
    expect(classifyTerminalKind(booking!.error.message)).toBe("not_pipeline_actionable");
    expect(booking!.error.message).toContain("#1750");
    // The gate's evidence survives for whoever reads the record.
    expect(booking!.error.message).toContain("dev context records zero file changes");
  });

  it("leaves an unscoped run's no-op a failure", () => {
    expect(HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", noOp, false)).toBeNull();
    expect(classifyTerminalKind(noOp.error.message)).toBe("premature_turn_end");
  });

  it("only applies to a feature-dev gate no-op", () => {
    expect(
      HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-planning", noOp, true)
    ).toBeNull();
    const fail = { error: new Error("stage gate failed: build failed"), noOp: false };
    expect(HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", fail, true)).toBeNull();
  });

  it("relaps once on the binary's relap decision (#1051)", () => {
    const relap = HeadlessOrchestrator.partialRerunNoOpBooking(1051, "feature-dev", noOp, true, {
      action: "relap",
      steer: "The plan is binding: implement a.go",
    });
    expect(relap).toEqual({ action: "relap", steer: "The plan is binding: implement a.go" });
  });

  it("ends a named dispute with the binary's detail", () => {
    const detail =
      "[not-pipeline-actionable] feature-dev: partial-delivery re-run of #1051 (after PR #9) disputed the plan instead of changing it: Task 2 [#3163]";
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(1051, "feature-dev", noOp, true, {
      action: "dispute",
      detail,
    });
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.error.message).toContain("Task 2");
    expect(classifyTerminalKind(booking.error.message)).toBe("not_pipeline_actionable");
  });

  it("keeps the gate verdict when the binary says the run is not scoped", () => {
    expect(
      HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", noOp, true, {
        action: "not_scoped",
      })
    ).toBeNull();
  });
});
