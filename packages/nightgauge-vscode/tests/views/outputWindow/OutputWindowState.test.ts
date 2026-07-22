import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

describe("OutputWindowState", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    // Create state without workspace storage (in-memory only)
    state = new OutputWindowState();
  });

  describe("Tool Call Aggregation", () => {
    describe("addToolCall", () => {
      it("should increment count for a tool type", () => {
        state.addToolCall("Edit");
        expect(state.getToolCallCount("Edit")).toBe(1);

        state.addToolCall("Edit");
        expect(state.getToolCallCount("Edit")).toBe(2);
      });

      it("should track multiple tool types independently", () => {
        state.addToolCall("Edit");
        state.addToolCall("Edit");
        state.addToolCall("Read");
        state.addToolCall("Bash");

        expect(state.getToolCallCount("Edit")).toBe(2);
        expect(state.getToolCallCount("Read")).toBe(1);
        expect(state.getToolCallCount("Bash")).toBe(1);
      });

      it("should initialize startedAt on first tool call", () => {
        const summary1 = state.getToolSummary();
        const initialStartedAt = summary1.startedAt;

        state.addToolCall("Edit");

        const summary2 = state.getToolSummary();
        expect(summary2.startedAt.getTime()).toBeLessThanOrEqual(Date.now());
      });
    });

    describe("getToolSummary", () => {
      it("should return empty summary initially", () => {
        const summary = state.getToolSummary();

        expect(summary.total).toBe(0);
        expect(summary.byTool.size).toBe(0);
      });

      it("should return correct total", () => {
        state.addToolCall("Edit");
        state.addToolCall("Read");
        state.addToolCall("Edit");
        state.addToolCall("Bash");
        state.addToolCall("Edit");

        const summary = state.getToolSummary();
        expect(summary.total).toBe(5);
      });

      it("should return correct breakdown by tool", () => {
        state.addToolCall("Edit");
        state.addToolCall("Edit");
        state.addToolCall("Read");

        const summary = state.getToolSummary();
        expect(summary.byTool.get("Edit")).toBe(2);
        expect(summary.byTool.get("Read")).toBe(1);
      });

      it("should return a copy of the Map (not the original)", () => {
        state.addToolCall("Edit");
        const summary = state.getToolSummary();

        summary.byTool.set("Bash", 100);

        expect(state.getToolCallCount("Bash")).toBe(0);
      });
    });

    describe("getToolCallCount", () => {
      it("should return 0 for tools that were never called", () => {
        expect(state.getToolCallCount("WebFetch")).toBe(0);
        expect(state.getToolCallCount("Unknown")).toBe(0);
      });

      it("should return correct count after multiple calls", () => {
        state.addToolCall("Glob");
        state.addToolCall("Glob");
        state.addToolCall("Glob");

        expect(state.getToolCallCount("Glob")).toBe(3);
      });
    });

    describe("getTotalToolCalls", () => {
      it("should return 0 initially", () => {
        expect(state.getTotalToolCalls()).toBe(0);
      });

      it("should return total across all tool types", () => {
        state.addToolCall("Edit");
        state.addToolCall("Read");
        state.addToolCall("Write");
        state.addToolCall("Bash");
        state.addToolCall("Edit");

        expect(state.getTotalToolCalls()).toBe(5);
      });
    });

    describe("resetToolCalls", () => {
      it("should clear all tool counts", () => {
        state.addToolCall("Edit");
        state.addToolCall("Read");
        state.addToolCall("Bash");

        state.resetToolCalls();

        expect(state.getTotalToolCalls()).toBe(0);
        expect(state.getToolCallCount("Edit")).toBe(0);
        expect(state.getToolCallCount("Read")).toBe(0);
        expect(state.getToolCallCount("Bash")).toBe(0);
      });

      it("should reset startedAt", () => {
        state.addToolCall("Edit");
        const summaryBefore = state.getToolSummary();

        state.resetToolCalls();

        // After reset, adding a new tool call should create a new startedAt
        state.addToolCall("Read");
        const summaryAfter = state.getToolSummary();

        expect(summaryAfter.startedAt.getTime()).toBeGreaterThanOrEqual(
          summaryBefore.startedAt.getTime()
        );
      });
    });

    describe("clear", () => {
      it("should also reset tool calls", () => {
        state.addToolCall("Edit");
        state.addToolCall("Read");

        state.clear();

        expect(state.getTotalToolCalls()).toBe(0);
      });
    });
  });

  describe("Output Entries", () => {
    it("should add and retrieve entries", () => {
      state.addEntry("Test message", "info");
      const entries = state.getEntries();

      expect(entries.length).toBe(1);
      expect(entries[0].text).toBe("Test message");
      expect(entries[0].level).toBe("info");
    });

    it("should generate unique IDs for entries", () => {
      state.addEntry("First", "info");
      state.addEntry("Second", "info");
      const entries = state.getEntries();

      expect(entries[0].id).not.toBe(entries[1].id);
    });

    describe("getEntryCount", () => {
      it("should return 0 when no entries exist", () => {
        expect(state.getEntryCount()).toBe(0);
      });

      it("should return correct count after adding entries", () => {
        state.addEntry("First", "info");
        state.addEntry("Second", "warning");
        state.addEntry("Third", "error");

        expect(state.getEntryCount()).toBe(3);
      });

      it("should return 0 after clearing", () => {
        state.addEntry("First", "info");
        state.addEntry("Second", "info");
        state.clear();

        expect(state.getEntryCount()).toBe(0);
      });
    });
  });

  describe("Auto-Scroll (Issue #159)", () => {
    it("should default to true", () => {
      expect(state.getAutoScroll()).toBe(true);
    });

    it("should allow setting auto-scroll preference", () => {
      state.setAutoScroll(false);
      expect(state.getAutoScroll()).toBe(false);

      state.setAutoScroll(true);
      expect(state.getAutoScroll()).toBe(true);
    });

    it("should persist across clear()", () => {
      state.setAutoScroll(false);
      state.clear();
      // Auto-scroll is a display preference that persists,
      // not pipeline state that gets reset
      expect(state.getAutoScroll()).toBe(false);
    });
  });

  describe("Word Wrap (Issue #161)", () => {
    it("should default to true", () => {
      expect(state.getWordWrap()).toBe(true);
    });

    it("should allow setting word wrap preference", () => {
      state.setWordWrap(false);
      expect(state.getWordWrap()).toBe(false);

      state.setWordWrap(true);
      expect(state.getWordWrap()).toBe(true);
    });

    it("should persist across clear()", () => {
      state.setWordWrap(false);
      state.clear();
      // Word wrap (like autoScroll) is a display preference that persists,
      // not pipeline state that gets reset
      expect(state.getWordWrap()).toBe(false);
    });
  });

  describe("Execution Mode (Issue #496)", () => {
    it("should default to headless mode", () => {
      expect(state.getExecutionMode()).toBe("headless");
    });

    it("should allow setting execution mode to interactive", () => {
      state.setExecutionMode("interactive");
      expect(state.getExecutionMode()).toBe("interactive");
    });

    it("should allow setting execution mode back to headless", () => {
      state.setExecutionMode("interactive");
      state.setExecutionMode("headless");
      expect(state.getExecutionMode()).toBe("headless");
    });

    it("should reset execution mode to headless on clear()", () => {
      // Execution mode resets to default (headless) when state is cleared
      // This matches pipeline behavior - new runs start in headless mode
      state.setExecutionMode("interactive");
      state.clear();
      expect(state.getExecutionMode()).toBe("headless");
    });
  });

  describe("Stage Progress", () => {
    it("should initialize all stages as pending", () => {
      const progress = state.getAllStageProgress();

      // 8 stages: pipeline-start, issue-pickup, feature-planning, feature-dev,
      // feature-validate, pr-create, pr-merge, pipeline-finish (Issue #284)
      expect(progress.length).toBe(8);
      progress.forEach((p) => {
        expect(p.status).toBe("pending");
      });
    });

    it("should update stage status", () => {
      state.updateStageStatus("issue-pickup", "running");
      const progress = state.getStageProgress("issue-pickup");

      expect(progress?.status).toBe("running");
      expect(progress?.startedAt).toBeInstanceOf(Date);
    });

    it("should track completion time and duration", async () => {
      state.updateStageStatus("feature-planning", "running");

      // Simulate some time passing
      await new Promise((resolve) => {
        setTimeout(() => {
          state.updateStageStatus("feature-planning", "complete");
          const progress = state.getStageProgress("feature-planning");

          expect(progress?.status).toBe("complete");
          expect(progress?.completedAt).toBeInstanceOf(Date);
          expect(progress?.durationMs).toBeGreaterThanOrEqual(0);
          resolve(undefined);
        }, 10);
      });
    });
  });

  describe("removeStallWarningEntries (Issue #797)", () => {
    it("should remove stall warning entries for the given stage", () => {
      state.addEntry(
        "[skillRunner] Stage still running after 5m 0s. This may be normal...",
        "warning",
        "feature-dev"
      );
      state.addEntry("Normal output line", "info", "feature-dev");
      state.addEntry(
        "[skillRunner] Stage still running after 10m 0s (2x threshold).",
        "warning",
        "feature-dev"
      );

      expect(state.getEntries()).toHaveLength(3);

      state.removeStallWarningEntries("feature-dev");

      const remaining = state.getEntries();
      expect(remaining).toHaveLength(1);
      expect(remaining[0].text).toBe("Normal output line");
    });

    it("should not remove entries from other stages", () => {
      state.addEntry("[skillRunner] Stage still running after 5m 0s.", "warning", "issue-pickup");
      state.addEntry("[skillRunner] Stage still running after 5m 0s.", "warning", "feature-dev");
      state.addEntry("Regular line", "info", "feature-dev");

      state.removeStallWarningEntries("feature-dev");

      const remaining = state.getEntries();
      expect(remaining).toHaveLength(2);
      expect(remaining[0].stage).toBe("issue-pickup");
      expect(remaining[1].text).toBe("Regular line");
    });

    it("should be a no-op when no stall warnings exist", () => {
      state.addEntry("Line 1", "info", "feature-dev");
      state.addEntry("Line 2", "warning", "feature-dev");

      state.removeStallWarningEntries("feature-dev");

      expect(state.getEntries()).toHaveLength(2);
    });
  });

  // =========================================================================
  // Per-slot buffer management (Issue #2705)
  // =========================================================================

  describe("Per-slot buffer management", () => {
    describe("registerSlot", () => {
      it("should register a slot and make it visible in getActiveSlots", () => {
        state.registerSlot(0, 2705, "Feature Planning");
        const slots = state.getActiveSlots();
        expect(slots).toHaveLength(1);
        expect(slots[0]).toMatchObject({
          slotIndex: 0,
          issueNumber: 2705,
          title: "Feature Planning",
        });
      });

      it("should be idempotent — updating title on re-register", () => {
        state.registerSlot(0, 2705, "Old Title");
        state.registerSlot(0, 2705, "New Title");
        expect(state.getActiveSlots()).toHaveLength(1);
        expect(state.getActiveSlots()[0].title).toBe("New Title");
      });

      it("should preserve stage when re-registering", () => {
        state.registerSlot(0, 2705, "Title");
        state.updateSlotStage(0, "feature-planning");
        state.registerSlot(0, 2705, "New Title");
        expect(state.getActiveSlots()[0].stage).toBe("feature-planning");
      });
    });

    describe("addEntry with slotIndex", () => {
      it("should route entry to the per-slot buffer", () => {
        state.registerSlot(0, 2705, "Slot A");
        state.addEntry("Slot A output", "info", "feature-dev", { slotIndex: 0 });

        const slotEntries = state.getSlotEntries(0);
        expect(slotEntries).toHaveLength(1);
        expect(slotEntries[0].text).toBe("Slot A output");
        expect(slotEntries[0].slotIndex).toBe(0);
      });

      it("should also add entry to the aggregated buffer", () => {
        state.addEntry("Slot B output", "info", "feature-dev", { slotIndex: 1 });

        const all = state.getEntries();
        expect(all).toHaveLength(1);
        expect(all[0].text).toBe("Slot B output");
      });

      it("should keep slot buffers independent", () => {
        state.addEntry("Slot 0 line", "info", undefined, { slotIndex: 0 });
        state.addEntry("Slot 1 line", "info", undefined, { slotIndex: 1 });
        state.addEntry("No slot line", "info");

        expect(state.getSlotEntries(0)).toHaveLength(1);
        expect(state.getSlotEntries(1)).toHaveLength(1);
        expect(state.getEntries()).toHaveLength(3);
      });

      it("should not add to slot buffer when slotIndex is undefined", () => {
        state.addEntry("No slot", "info");
        state.addEntry("Also no slot", "debug");

        expect(state.getSlotEntries(0)).toHaveLength(0);
        expect(state.getSlotEntries(null)).toHaveLength(2);
      });
    });

    describe("getSlotEntries", () => {
      it("should return aggregated entries when slotIndex is null", () => {
        state.addEntry("A", "info", undefined, { slotIndex: 0 });
        state.addEntry("B", "info", undefined, { slotIndex: 1 });
        state.addEntry("C", "info");

        const all = state.getSlotEntries(null);
        expect(all).toHaveLength(3);
      });

      it("should return only slot-specific entries for a given slotIndex", () => {
        state.addEntry("Slot 0 A", "info", undefined, { slotIndex: 0 });
        state.addEntry("Slot 0 B", "info", undefined, { slotIndex: 0 });
        state.addEntry("Slot 1 A", "info", undefined, { slotIndex: 1 });

        expect(state.getSlotEntries(0)).toHaveLength(2);
        expect(state.getSlotEntries(1)).toHaveLength(1);
      });

      it("should return an empty array for an unknown slotIndex", () => {
        expect(state.getSlotEntries(99)).toHaveLength(0);
      });

      it("should return a copy, not a reference", () => {
        state.addEntry("X", "info", undefined, { slotIndex: 0 });
        const entries = state.getSlotEntries(0);
        entries.push({ id: "fake", timestamp: new Date(), level: "info", text: "injected" });
        expect(state.getSlotEntries(0)).toHaveLength(1);
      });
    });

    describe("setActiveSlot / getActiveSlotIndex", () => {
      it("should default to null (All tab)", () => {
        expect(state.getActiveSlotIndex()).toBeNull();
      });

      it("should set and retrieve the active slot index", () => {
        state.setActiveSlot(2);
        expect(state.getActiveSlotIndex()).toBe(2);
      });

      it("should allow switching back to null (All tab)", () => {
        state.setActiveSlot(1);
        state.setActiveSlot(null);
        expect(state.getActiveSlotIndex()).toBeNull();
      });
    });

    describe("getActiveSlots", () => {
      it("should return an empty array when no slots are registered", () => {
        expect(state.getActiveSlots()).toHaveLength(0);
      });

      it("should return slots sorted by slotIndex", () => {
        state.registerSlot(2, 2707, "C");
        state.registerSlot(0, 2705, "A");
        state.registerSlot(1, 2706, "B");

        const slots = state.getActiveSlots();
        expect(slots.map((s) => s.slotIndex)).toEqual([0, 1, 2]);
      });
    });

    describe("updateSlotStage", () => {
      it("should update the stage label for the slot", () => {
        state.registerSlot(0, 2705, "Title");
        state.updateSlotStage(0, "feature-dev");

        expect(state.getActiveSlots()[0].stage).toBe("feature-dev");
      });

      it("should be a no-op for unknown slot index", () => {
        // Should not throw
        expect(() => state.updateSlotStage(99, "feature-dev")).not.toThrow();
      });
    });

    describe("clear resets per-slot state", () => {
      it("should clear per-slot buffers and slot infos on clear()", () => {
        state.registerSlot(0, 2705, "Title");
        state.addEntry("Slot output", "info", undefined, { slotIndex: 0 });
        state.setActiveSlot(0);

        state.clear();

        expect(state.getActiveSlots()).toHaveLength(0);
        expect(state.getSlotEntries(0)).toHaveLength(0);
        expect(state.getActiveSlotIndex()).toBeNull();
      });
    });
  });

  describe("Per-slot stage and token routing", () => {
    it("two slots have independent tokenUsage totals", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.registerSlot(1, 101, "Issue #101");

      state.setSlotTokenUsage(0, {
        inputTokens: 1000,
        outputTokens: 500,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.01,
      });
      state.setSlotTokenUsage(1, {
        inputTokens: 2000,
        outputTokens: 800,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.02,
      });

      expect(state.getSlotTokenUsage(0).inputTokens).toBe(1000);
      expect(state.getSlotTokenUsage(1).inputTokens).toBe(2000);
      expect(state.getSlotTokenUsage(0).outputTokens).toBe(500);
      expect(state.getSlotTokenUsage(1).outputTokens).toBe(800);
    });

    it("updating slot 0 stages does not affect slot 1 stages", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.registerSlot(1, 101, "Issue #101");

      state.updateSlotStageStatus(0, "issue-pickup", "complete");
      state.updateSlotStageStatus(1, "issue-pickup", "running");

      const slot0Stages = state.getSlotStageProgress(0);
      const slot1Stages = state.getSlotStageProgress(1);

      const pickup0 = slot0Stages.find((s) => s.stage === "issue-pickup");
      const pickup1 = slot1Stages.find((s) => s.stage === "issue-pickup");

      expect(pickup0?.status).toBe("complete");
      expect(pickup1?.status).toBe("running");
    });

    it("addEntry routes to active slot buffer when no slotIndex provided", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.setActiveSlot(0);

      state.addEntry("hello from issue 100", "info");

      expect(state.getSlotEntries(0)).toHaveLength(1);
      expect(state.getSlotEntries(0)[0].text).toBe("hello from issue 100");
    });

    it("addEntry with explicit slotIndex overrides active slot", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.registerSlot(1, 101, "Issue #101");
      state.setActiveSlot(0);

      state.addEntry("explicit slot 1 entry", "info", undefined, { slotIndex: 1 });

      expect(state.getSlotEntries(1)).toHaveLength(1);
      expect(state.getSlotEntries(0)).toHaveLength(0);
    });

    it("clearSlot removes that slot's entries without affecting other slots", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.registerSlot(1, 101, "Issue #101");

      state.addEntry("slot0 entry", "info", undefined, { slotIndex: 0 });
      state.addEntry("slot1 entry", "info", undefined, { slotIndex: 1 });

      state.clearSlot(0);

      expect(state.getSlotEntries(0)).toHaveLength(0);
      expect(state.getSlotEntries(1)).toHaveLength(1);
    });

    it("clearSlot resets slot stages to pending", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.updateSlotStageStatus(0, "issue-pickup", "complete");
      state.updateSlotStageStatus(0, "feature-planning", "running");

      state.clearSlot(0);

      const stages = state.getSlotStageProgress(0);
      expect(stages.every((s) => s.status === "pending")).toBe(true);
    });

    it("clearSlot resets slot tokenUsage to zero", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.setSlotTokenUsage(0, {
        inputTokens: 999,
        outputTokens: 111,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.05,
      });

      state.clearSlot(0);

      const usage = state.getSlotTokenUsage(0);
      expect(usage.inputTokens).toBe(0);
      expect(usage.costUsd).toBe(0);
    });

    it("findSlotIndexByIssue returns correct slot", () => {
      state.registerSlot(0, 100, "Issue #100");
      state.registerSlot(1, 101, "Issue #101");

      expect(state.findSlotIndexByIssue(100)).toBe(0);
      expect(state.findSlotIndexByIssue(101)).toBe(1);
      expect(state.findSlotIndexByIssue(999)).toBeUndefined();
    });

    it("getSlotTokenUsage returns zero defaults for unregistered slot", () => {
      const usage = state.getSlotTokenUsage(99);

      expect(usage.inputTokens).toBe(0);
      expect(usage.outputTokens).toBe(0);
      expect(usage.costUsd).toBe(0);
    });

    it("getSlotStageProgress returns empty array for unregistered slot", () => {
      expect(state.getSlotStageProgress(99)).toHaveLength(0);
    });
  });
});
