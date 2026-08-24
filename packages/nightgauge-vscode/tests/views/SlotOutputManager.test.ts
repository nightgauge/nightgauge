/**
 * Tests for SlotOutputManager (Issue #1635)
 *
 * Verifies that revealSlotChannel() correctly reveals the output channel
 * for a given slot index.
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn((name: string) => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
      name,
    })),
  },
}));

import { SlotOutputManager } from "../../src/views/SlotOutputManager";

describe("SlotOutputManager - revealSlotChannel", () => {
  let manager: SlotOutputManager;

  beforeEach(() => {
    manager = new SlotOutputManager();
    vi.clearAllMocks();
  });

  it("reveals the output channel for the matching slot index", () => {
    const channel = manager.createSlotChannel(1, 1635, "Test Issue");

    manager.revealSlotChannel(1);

    expect(channel.show).toHaveBeenCalledWith(true);
  });

  it("does nothing when no channel matches the slot index", () => {
    expect(() => manager.revealSlotChannel(99)).not.toThrow();
  });

  it("reveals the correct channel when multiple slots exist", () => {
    const ch0 = manager.createSlotChannel(0, 100, "Issue 100");
    const ch1 = manager.createSlotChannel(1, 200, "Issue 200");

    manager.revealSlotChannel(1);

    expect(ch1.show).toHaveBeenCalledWith(true);
    expect(ch0.show).not.toHaveBeenCalled();
  });
});

// #230: the slot-started seed and the first stage-changed event both target
// issue-pickup, which used to print the stage banner twice. updateStage must be
// idempotent per stage.
describe("SlotOutputManager - updateStage idempotency (#230)", () => {
  let manager: SlotOutputManager;
  let onStageChanged: Mock;

  beforeEach(() => {
    manager = new SlotOutputManager();
    vi.clearAllMocks();
    onStageChanged = vi.fn();
    manager.setCallbacks({ onStageChanged });
  });

  it("emits the stage banner and callback once for a repeated stage", () => {
    const channel = manager.createSlotChannel(0, 244, "Dogfood");
    vi.mocked(channel.appendLine).mockClear();

    manager.updateStage(244, "issue-pickup");
    manager.updateStage(244, "issue-pickup"); // duplicate seed → no-op

    // Two appendLine calls per real transition ("" + "--- Stage: … ---").
    expect(channel.appendLine).toHaveBeenCalledTimes(2);
    expect(onStageChanged).toHaveBeenCalledTimes(1);
  });

  it("still emits when the stage genuinely changes", () => {
    const channel = manager.createSlotChannel(0, 244, "Dogfood");
    vi.mocked(channel.appendLine).mockClear();

    manager.updateStage(244, "issue-pickup");
    manager.updateStage(244, "issue-pickup");
    manager.updateStage(244, "feature-planning");

    expect(onStageChanged).toHaveBeenCalledTimes(2);
    expect(onStageChanged).toHaveBeenNthCalledWith(1, 0, 244, "issue-pickup");
    expect(onStageChanged).toHaveBeenNthCalledWith(2, 0, 244, "feature-planning");
  });
});

// #283 defect 2: the slot's current-stage pointer advances early (the #981
// spinner fires before the previous stage's gate runs), so consumers that
// re-derive the stage from slot state mis-file end-of-stage diagnostics under
// the NEXT stage — #127's gate-not-invoked audit about issue-pickup printed
// as "[feature-validate]". The EMITTING stage must be threaded through
// appendOutput/appendError to the aggregation callback verbatim.
describe("SlotOutputManager - stage threading (#283)", () => {
  let manager: SlotOutputManager;
  let onOutput: Mock;

  beforeEach(() => {
    manager = new SlotOutputManager();
    onOutput = vi.fn();
    manager.setCallbacks({ onOutput });
    vi.clearAllMocks();
  });

  it("threads the emitting stage through appendOutput", () => {
    manager.createSlotChannel(0, 127, "Issue 127");

    manager.appendOutput(127, "[gate-not-invoked] stage=issue-pickup …", "issue-pickup");

    expect(onOutput).toHaveBeenCalledWith(
      0,
      127,
      "[gate-not-invoked] stage=issue-pickup …",
      "info",
      "issue-pickup"
    );
  });

  it("threads the emitting stage through appendError", () => {
    manager.createSlotChannel(1, 127, "Issue 127");

    manager.appendError(127, "gate failed", "feature-dev");

    expect(onOutput).toHaveBeenCalledWith(1, 127, "gate failed", "error", "feature-dev");
  });

  it("passes undefined stage through unchanged so consumers may fall back", () => {
    manager.createSlotChannel(0, 127, "Issue 127");

    manager.appendOutput(127, "no stage in scope");

    expect(onOutput).toHaveBeenCalledWith(0, 127, "no stage in scope", "info", undefined);
  });
});
