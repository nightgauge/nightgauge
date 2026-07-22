/**
 * Unit tests for statusInference utility
 *
 * Tests all 17 inference scenarios for the 4-step chain:
 *   1. Pipeline execution state
 *   2. Board status (pass-through)
 *   3. Label fallback
 *   4. Default readiness rules
 *
 * Also tests projectFieldMapping additions:
 *   - extractStatusLabel
 *   - mapStatusLabel
 *   - isStatusLabel
 *
 * @see Issue #2570
 */

import { describe, it, expect } from "vitest";
import { inferWorkItemStatus, type StatusInferenceInput } from "../../src/utils/statusInference";
import {
  extractStatusLabel,
  mapStatusLabel,
  isStatusLabel,
} from "../../src/utils/projectFieldMapping";
import type { PipelineState } from "../../src/services/PipelineStateService";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeItem(overrides: Partial<StatusInferenceInput> = {}): StatusInferenceInput {
  return {
    number: 42,
    labels: [],
    ...overrides,
  };
}

function makePipelineState(
  issueNumber: number,
  stageStatuses: Array<"pending" | "running" | "complete" | "failed" | "skipped" | "deferred">
): PipelineState {
  const stages: PipelineState["stages"] = {};
  stageStatuses.forEach((status, i) => {
    stages[`stage-${i}`] = { status };
  });
  return {
    issue_number: issueNumber,
    title: "Test issue",
    branch: `feat/${issueNumber}-test`,
    stages,
    started_at: "2026-01-01T00:00:00Z",
  };
}

// ---------------------------------------------------------------------------
// inferWorkItemStatus — Step 1: Pipeline execution state
// ---------------------------------------------------------------------------

describe("inferWorkItemStatus", () => {
  describe("Step 1: Pipeline execution state", () => {
    it("returns 'In progress' when issue is actively running in pipeline", () => {
      const pipelineState = makePipelineState(42, ["complete", "running", "pending"]);
      expect(inferWorkItemStatus(makeItem({ number: 42 }), pipelineState)).toBe("In progress");
    });

    it("falls through when pipeline state is for a different issue", () => {
      const pipelineState = makePipelineState(99, ["running"]);
      // Falls to default readiness rules (open + unblocked → Ready)
      expect(inferWorkItemStatus(makeItem({ number: 42 }), pipelineState)).toBe("Ready");
    });

    it("falls through when pipeline has no running stages", () => {
      const pipelineState = makePipelineState(42, ["complete", "pending"]);
      // Falls to default readiness rules (open + unblocked → Ready)
      expect(inferWorkItemStatus(makeItem({ number: 42 }), pipelineState)).toBe("Ready");
    });

    it("falls through when pipelineState is undefined", () => {
      expect(inferWorkItemStatus(makeItem({ number: 42 }), undefined)).toBe("Ready");
    });

    it("falls through when pipelineState is null", () => {
      expect(inferWorkItemStatus(makeItem({ number: 42 }), null)).toBe("Ready");
    });
  });

  // -------------------------------------------------------------------------
  // Step 2: Board status pass-through
  // -------------------------------------------------------------------------

  describe("Step 2: Board status pass-through", () => {
    it("returns board status when status is 'Ready' and valid", () => {
      expect(inferWorkItemStatus(makeItem({ status: "Ready" }))).toBe("Ready");
    });

    it("returns board status when status is 'In progress' and valid", () => {
      expect(inferWorkItemStatus(makeItem({ status: "In progress" }))).toBe("In progress");
    });

    it("returns board status when status is 'Backlog' and valid", () => {
      expect(inferWorkItemStatus(makeItem({ status: "Backlog" }))).toBe("Backlog");
    });

    it("returns board status when status is 'Done' and valid", () => {
      expect(inferWorkItemStatus(makeItem({ status: "Done" }))).toBe("Done");
    });

    it("falls through when status is empty string", () => {
      // Empty string is not a valid ProjectBoardStatus, falls to labels/default
      expect(inferWorkItemStatus(makeItem({ status: "" }))).toBe("Ready");
    });

    it("falls through when status is undefined", () => {
      expect(inferWorkItemStatus(makeItem({ status: undefined }))).toBe("Ready");
    });
  });

  // -------------------------------------------------------------------------
  // Step 3: Label fallback
  // -------------------------------------------------------------------------

  describe("Step 3: Label fallback", () => {
    it("returns 'Ready' for status:ready label", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["status:ready"] }))).toBe("Ready");
    });

    it("returns 'Backlog' for status:backlog label", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["status:backlog"] }))).toBe("Backlog");
    });

    it("returns 'In progress' for status:in-progress label", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["status:in-progress"] }))).toBe("In progress");
    });

    it("returns 'In review' for status:in-review label", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["status:in-review"] }))).toBe("In review");
    });

    it("returns 'Done' for status:done label", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["status:done"] }))).toBe("Done");
    });

    it("falls through to defaults for unknown labels", () => {
      expect(inferWorkItemStatus(makeItem({ labels: ["type:feature", "priority:high"] }))).toBe(
        "Ready"
      );
    });

    it("uses first matching status label when multiple status labels present", () => {
      // extractStatusLabel returns the first match
      expect(inferWorkItemStatus(makeItem({ labels: ["status:backlog", "status:ready"] }))).toBe(
        "Backlog"
      );
    });

    it("status label wins over blocked state (Step 3 before Step 4)", () => {
      // Even if blocked by open issue, label takes precedence
      expect(
        inferWorkItemStatus(
          makeItem({
            labels: ["status:ready"],
            blockedBy: [{ state: "OPEN" }],
          })
        )
      ).toBe("Ready");
    });
  });

  // -------------------------------------------------------------------------
  // Step 4: Default readiness rules
  // -------------------------------------------------------------------------

  describe("Step 4: Default readiness rules", () => {
    it("returns 'Done' for CLOSED issue with no other context", () => {
      expect(inferWorkItemStatus(makeItem({ issueState: "CLOSED" }))).toBe("Done");
    });

    it("returns 'Backlog' for open issue blocked by open blocker", () => {
      expect(inferWorkItemStatus(makeItem({ blockedBy: [{ state: "OPEN" }] }))).toBe("Backlog");
    });

    it("returns 'Ready' for open issue blocked only by closed blocker", () => {
      // Closed blocker does not block progress
      expect(inferWorkItemStatus(makeItem({ blockedBy: [{ state: "CLOSED" }] }))).toBe("Ready");
    });

    it("returns 'Ready' for open issue with no labels and no blockers (default)", () => {
      expect(inferWorkItemStatus(makeItem())).toBe("Ready");
    });

    it("returns 'Ready' for open issue with empty labels array", () => {
      expect(inferWorkItemStatus(makeItem({ labels: [] }))).toBe("Ready");
    });
  });

  // -------------------------------------------------------------------------
  // Regression cases
  // -------------------------------------------------------------------------

  describe("regression cases", () => {
    it("returns 'Ready' for partially synced item (status undefined, no labels, no blockers)", () => {
      // AC: Items without board membership fall back to Ready by default
      expect(
        inferWorkItemStatus({
          number: 123,
          labels: [],
          status: undefined,
          blockedBy: undefined,
          issueState: undefined,
        })
      ).toBe("Ready");
    });

    it("pipeline state takes precedence over board status", () => {
      const pipelineState = makePipelineState(42, ["running"]);
      // Even though board status says Backlog, pipeline running wins
      expect(inferWorkItemStatus(makeItem({ number: 42, status: "Backlog" }), pipelineState)).toBe(
        "In progress"
      );
    });

    it("pipeline state takes precedence over labels", () => {
      const pipelineState = makePipelineState(42, ["running"]);
      expect(
        inferWorkItemStatus(makeItem({ number: 42, labels: ["status:backlog"] }), pipelineState)
      ).toBe("In progress");
    });
  });
});

// ---------------------------------------------------------------------------
// projectFieldMapping additions
// ---------------------------------------------------------------------------

describe("projectFieldMapping — StatusLabel additions", () => {
  describe("extractStatusLabel", () => {
    it("extracts status:ready from mixed labels", () => {
      expect(extractStatusLabel(["type:feature", "status:ready", "size:M"])).toBe("status:ready");
    });

    it("extracts status:in-progress", () => {
      expect(extractStatusLabel(["status:in-progress"])).toBe("status:in-progress");
    });

    it("extracts status:in-review", () => {
      expect(extractStatusLabel(["status:in-review"])).toBe("status:in-review");
    });

    it("extracts status:done", () => {
      expect(extractStatusLabel(["status:done"])).toBe("status:done");
    });

    it("extracts status:backlog", () => {
      expect(extractStatusLabel(["status:backlog"])).toBe("status:backlog");
    });

    it("returns undefined when no status label present", () => {
      expect(extractStatusLabel(["type:bug", "priority:high", "size:S"])).toBeUndefined();
    });

    it("returns undefined for empty array", () => {
      expect(extractStatusLabel([])).toBeUndefined();
    });

    it("returns first status label when multiple are present", () => {
      expect(extractStatusLabel(["status:backlog", "status:ready"])).toBe("status:backlog");
    });
  });

  describe("mapStatusLabel", () => {
    it("maps status:ready to Ready", () => {
      expect(mapStatusLabel("status:ready")).toBe("Ready");
    });

    it("maps status:in-progress to In progress", () => {
      expect(mapStatusLabel("status:in-progress")).toBe("In progress");
    });

    it("maps status:in-review to In review", () => {
      expect(mapStatusLabel("status:in-review")).toBe("In review");
    });

    it("maps status:done to Done", () => {
      expect(mapStatusLabel("status:done")).toBe("Done");
    });

    it("maps status:backlog to Backlog", () => {
      expect(mapStatusLabel("status:backlog")).toBe("Backlog");
    });

    it("returns empty string for unknown label", () => {
      expect(mapStatusLabel("status:unknown")).toBe("");
    });

    it("returns empty string for null", () => {
      expect(mapStatusLabel(null)).toBe("");
    });

    it("returns empty string for undefined", () => {
      expect(mapStatusLabel(undefined)).toBe("");
    });

    it("returns empty string for empty string", () => {
      expect(mapStatusLabel("")).toBe("");
    });
  });

  describe("isStatusLabel", () => {
    it("returns true for all valid status labels", () => {
      expect(isStatusLabel("status:ready")).toBe(true);
      expect(isStatusLabel("status:in-progress")).toBe(true);
      expect(isStatusLabel("status:in-review")).toBe(true);
      expect(isStatusLabel("status:done")).toBe(true);
      expect(isStatusLabel("status:backlog")).toBe(true);
    });

    it("returns false for non-status labels", () => {
      expect(isStatusLabel("priority:high")).toBe(false);
      expect(isStatusLabel("size:M")).toBe(false);
      expect(isStatusLabel("type:feature")).toBe(false);
      expect(isStatusLabel("")).toBe(false);
    });

    it("is case-sensitive", () => {
      expect(isStatusLabel("Status:Ready")).toBe(false);
      expect(isStatusLabel("STATUS:READY")).toBe(false);
    });
  });
});
