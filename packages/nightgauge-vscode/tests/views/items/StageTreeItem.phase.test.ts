/**
 * StageTreeItem - Phase child management tests
 *
 * Tests the phase-related functionality added to StageTreeItem in Issue #1028.
 * Covers setPhases(), clearPhases(), getPhaseCount(), collapsible state rules,
 * formatDescription() with phase context, reset() clearing phases, and
 * getChildren() returning PhaseTreeItem instances.
 *
 * NOTE: This file does NOT duplicate the general StageTreeItem tests. It focuses
 * exclusively on the phase child management surface introduced by Issue #1028.
 *
 * @see Issue #1028 - Render phase progress as children in pipeline tree view
 */

import { describe, it, expect, beforeEach } from "vitest";
import * as vscode from "vscode";
import { StageTreeItem } from "../../../src/views/items/StageTreeItem";
import { PhaseTreeItem } from "../../../src/views/items/PhaseTreeItem";
import type { StagePhase } from "../../../src/schemas/pipelineState";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a minimal valid StagePhase object. */
function makePhase(name: string, status: StagePhase["status"] = "pending"): StagePhase {
  return { name, status };
}

/**
 * Build an array of phases that mimics a realistic feature-dev skill output.
 * 13 phases total with a spread of statuses that lets tests control exactly
 * which ones are "done".
 */
function makePhases(count: number, status: StagePhase["status"] = "pending"): StagePhase[] {
  const names = [
    "load-context",
    "read-planning-context",
    "analyze-changes",
    "setup-branch",
    "implement-core",
    "implementation",
    "write-tests",
    "run-tests",
    "fix-lint",
    "update-docs",
    "review-diff",
    "finalize",
    "verify",
  ];
  return names.slice(0, count).map((name) => makePhase(name, status));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("StageTreeItem — phase child management (Issue #1028)", () => {
  let item: StageTreeItem;

  beforeEach(() => {
    // Use 'feature-dev' as a representative non-bookend stage throughout.
    item = new StageTreeItem("feature-dev", "pending");
  });

  // -------------------------------------------------------------------------
  // setPhases()
  // -------------------------------------------------------------------------

  describe("setPhases()", () => {
    it("creates a PhaseTreeItem child for each phase supplied", () => {
      const phases = makePhases(3);
      item.setPhases(phases);

      const children = item.getChildren();
      expect(children).toHaveLength(3);
    });

    it("creates PhaseTreeItem instances (not plain TreeItems)", () => {
      const phases = makePhases(3);
      item.setPhases(phases);

      for (const child of item.getChildren()) {
        expect(child).toBeInstanceOf(PhaseTreeItem);
      }
    });

    it("assigns the correct status to each PhaseTreeItem child", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
        makePhase("write-tests", "pending"),
        makePhase("run-tests", "skipped"),
      ];
      item.setPhases(phases);

      const children = item.getChildren() as PhaseTreeItem[];
      expect(children[0].getStatus()).toBe("complete");
      expect(children[1].getStatus()).toBe("running");
      expect(children[2].getStatus()).toBe("pending");
      expect(children[3].getStatus()).toBe("skipped");
    });

    it("preserves the original kebab-case phaseName on each child", () => {
      const phases = [makePhase("load-context"), makePhase("read-planning-context")];
      item.setPhases(phases);

      const children = item.getChildren() as PhaseTreeItem[];
      expect(children[0].phaseName).toBe("load-context");
      expect(children[1].phaseName).toBe("read-planning-context");
    });

    it("replaces existing phases when called a second time", () => {
      item.setPhases(makePhases(5));
      // Now replace with a completely different set.
      const newPhases = [makePhase("finalize", "complete"), makePhase("verify", "running")];
      item.setPhases(newPhases);

      const children = item.getChildren() as PhaseTreeItem[];
      expect(children).toHaveLength(2);
      expect(children[0].phaseName).toBe("finalize");
      expect(children[1].phaseName).toBe("verify");
    });

    it("updates getPhaseCount() to reflect the new phase array length", () => {
      item.setPhases(makePhases(7));
      expect(item.getPhaseCount()).toBe(7);

      item.setPhases(makePhases(3));
      expect(item.getPhaseCount()).toBe(3);
    });

    it("accepts an optional currentPhase parameter without throwing", () => {
      const phases = makePhases(4);
      expect(() => item.setPhases(phases, "implementation")).not.toThrow();
    });
  });

  // -------------------------------------------------------------------------
  // clearPhases()
  // -------------------------------------------------------------------------

  describe("clearPhases()", () => {
    it("removes all children added by setPhases()", () => {
      item.setPhases(makePhases(5));
      item.clearPhases();

      expect(item.getChildren()).toHaveLength(0);
    });

    it("resets getPhaseCount() to zero", () => {
      item.setPhases(makePhases(5));
      item.clearPhases();

      expect(item.getPhaseCount()).toBe(0);
    });

    it("sets collapsible state back to None after clearing", () => {
      item.setStatus("running");
      item.setPhases(makePhases(4));
      // Sanity: should be Expanded while running with phases.
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      item.clearPhases();
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("is safe to call on an item that already has no phases", () => {
      expect(() => item.clearPhases()).not.toThrow();
      expect(item.getChildren()).toHaveLength(0);
      expect(item.getPhaseCount()).toBe(0);
    });
  });

  // -------------------------------------------------------------------------
  // Collapsible state rules
  // -------------------------------------------------------------------------

  describe("collapsible state rules", () => {
    it("is None on a fresh item with no phases", () => {
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("remains None after setStatus() when there are still no phases", () => {
      item.setStatus("running");
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("becomes Expanded when running AND has phases", () => {
      item.setStatus("running");
      item.setPhases(makePhases(3));

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
    });

    it("becomes Collapsed when complete AND has phases", () => {
      item.setPhases(makePhases(3));
      item.setStatus("complete");

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("becomes Collapsed when failed AND has phases", () => {
      item.setPhases(makePhases(3));
      item.setStatus("failed");

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("transitions from Expanded to Collapsed when status changes from running to complete", () => {
      item.setStatus("running");
      item.setPhases(makePhases(4));
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      item.setStatus("complete");
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("transitions from Expanded to None when phases are cleared while running", () => {
      item.setStatus("running");
      item.setPhases(makePhases(4));
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      item.clearPhases();
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });
  });

  // -------------------------------------------------------------------------
  // formatDescription() — phase-related variants
  // -------------------------------------------------------------------------

  describe("formatDescription() with phases", () => {
    it("shows registry phase progress when running before any phase marker arrives", () => {
      item.setStatus("running");
      // Opus/extended-thinking models can start a stage before the first
      // marker is surfaced. Keep the tree useful by showing the registry
      // fallback instead of a bare "running..." label.
      expect(item.description).toBe("Validate Environment [0/18]");
    });

    it("shows registry phase progress when running but currentPhase is absent", () => {
      // Providing phases but no currentPhase means currentPhaseName is null,
      // so the live phase branch falls back to the registry-defined first
      // phase rather than dropping phase/count display entirely.
      item.setStatus("running");
      item.setPhases(makePhases(13)); // no currentPhase argument
      expect(item.description).toBe("Validate Environment [0/18]");
    });

    it("shows phase progress when running with currentPhase and phases", () => {
      // Set up: 13 phases, 2 complete before the current one, current = "implementation".
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("read-planning-context", "complete"),
        makePhase("implementation", "running"),
        ...makePhases(10).map((p) => makePhase(p.name, "pending")),
      ].slice(0, 13);

      item.setStatus("running");
      item.setPhases(phases, "implementation");

      // completedCount = phases with status 'complete' or 'skipped' = 2
      // totalPhaseCount = 13
      // phaseLabel = 'Implementation' (kebab → Title Case)
      expect(item.description).toBe("Implementation [2/13]");
    });

    it("converts multi-word kebab currentPhase to Title Case in description", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("read-planning-context", "running"),
        makePhase("implementation", "pending"),
      ];
      item.setStatus("running");
      item.setPhases(phases, "read-planning-context");

      // 1 complete, 3 total, currentPhase label = 'Read Planning Context'
      expect(item.description).toBe("Read Planning Context [1/3]");
    });

    it("shows next registry phase when currentPhase is already complete (between-phases gap)", () => {
      // Simulates the window between phase N completing and phase N+1 emitting
      // its phase:start marker. currentPhaseName is "implementation" (index 7)
      // but that PhaseTreeItem is already complete, so the description should
      // show the next registry phase ("Testing") rather than the stale one.
      const phases: StagePhase[] = [
        makePhase("validate-environment", "complete"),
        makePhase("read-planning-context", "complete"),
        makePhase("batch-plan-detection", "complete"),
        makePhase("feedback-context-check", "complete"),
        makePhase("plan-verification", "complete"),
        makePhase("knowledge-base-read", "complete"),
        makePhase("standards-loading", "complete"),
        makePhase("implementation", "complete"), // completed, next hasn't started
      ];
      item.setStatus("running");
      item.setPhases(phases, "implementation", 18);

      // "implementation" is index 8 in feature-dev registry (was 7 with 17 phases) → next is "testing"
      expect(item.description).toBe("Testing [8/18]");
    });

    it('shows "running... [N/T]" when currentPhase is the last in the registry (no next phase)', () => {
      // Edge case: last phase completed but stage still running (shouldn't
      // normally happen, but must not crash).
      const phases: StagePhase[] = [makePhase("self-assessment", "complete")];
      item.setStatus("running");
      item.setPhases(phases, "self-assessment", 18);

      expect(item.description).toBe("running... [1/18]");
    });

    it("counts skipped phases as completed in the progress counter", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("setup-branch", "skipped"),
        makePhase("implementation", "running"),
        makePhase("write-tests", "pending"),
      ];
      item.setStatus("running");
      item.setPhases(phases, "implementation");

      // completedCount = complete(1) + skipped(1) = 2, total = 4
      expect(item.description).toBe("Implementation [2/4]");
    });

    it("shows compact phase summary when complete with phases and no token info", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
        makePhase("write-tests", "complete"),
      ];
      item.setPhases(phases);
      item.setStatus("complete");

      // completedCount = 3, totalPhaseCount = 3
      expect(item.description).toBe("3/3 phases");
    });

    it("shows compact phase summary when failed with phases", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "running"),
        makePhase("write-tests", "pending"),
      ];
      item.setPhases(phases);
      item.setStatus("failed");

      // completedCount = 1 (only 'complete' or 'skipped'), totalPhaseCount = 3
      expect(item.description).toBe("1/3 phases");
    });

    it("appends token info alongside phase summary when complete with token data", () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
      ];
      item.setPhases(phases);
      item.setTokenUsage({
        inputTokens: 1500,
        outputTokens: 800,
        costUsd: 0.0023,
      });
      item.setStatus("complete");

      // totalTokens = (1500 + 800) / 1000 = 2.3K, phaseSummary = '2/2 phases'
      expect(item.description).toBe("2/2 phases | $0.0023 | 2.3K tokens");
    });

    it('appends "tokens: N/A" alongside phase summary when complete in interactive mode', () => {
      const phases: StagePhase[] = [
        makePhase("load-context", "complete"),
        makePhase("implementation", "complete"),
      ];
      item.setPhases(phases);
      item.setExecutionMode("interactive");
      item.setStatus("complete");

      expect(item.description).toBe("2/2 phases | tokens: N/A");
    });
  });

  // -------------------------------------------------------------------------
  // reset() clears phases
  // -------------------------------------------------------------------------

  describe("reset()", () => {
    it("removes all phase children", () => {
      item.setPhases(makePhases(5));
      item.setStatus("running");
      item.reset();

      expect(item.getChildren()).toHaveLength(0);
    });

    it("resets getPhaseCount() to zero", () => {
      item.setPhases(makePhases(5));
      item.reset();

      expect(item.getPhaseCount()).toBe(0);
    });

    it("sets collapsible state back to None", () => {
      item.setStatus("running");
      item.setPhases(makePhases(3));
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);

      item.reset();
      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("resets status to pending", () => {
      item.setStatus("complete");
      item.reset();

      expect(item.getStatus()).toBe("pending");
    });
  });

  // -------------------------------------------------------------------------
  // getChildren() and getPhaseCount()
  // -------------------------------------------------------------------------

  describe("getChildren()", () => {
    it("returns an empty array before setPhases() is called", () => {
      expect(item.getChildren()).toHaveLength(0);
    });

    it("returns PhaseTreeItem instances after setPhases()", () => {
      item.setPhases(makePhases(4));

      const children = item.getChildren();
      expect(children).toHaveLength(4);
      for (const child of children) {
        expect(child).toBeInstanceOf(PhaseTreeItem);
      }
    });

    it("returns an empty array after clearPhases()", () => {
      item.setPhases(makePhases(4));
      item.clearPhases();

      expect(item.getChildren()).toHaveLength(0);
    });

    it("returns the latest set of children when setPhases() is called twice", () => {
      item.setPhases(makePhases(5));
      item.setPhases([makePhase("verify", "complete")]);

      const children = item.getChildren() as PhaseTreeItem[];
      expect(children).toHaveLength(1);
      expect(children[0].phaseName).toBe("verify");
    });
  });

  describe("getPhaseCount()", () => {
    it("returns 0 on a fresh item", () => {
      expect(item.getPhaseCount()).toBe(0);
    });

    it("returns the count matching the phases array passed to setPhases()", () => {
      item.setPhases(makePhases(13));
      expect(item.getPhaseCount()).toBe(13);
    });

    it("returns 0 after clearPhases()", () => {
      item.setPhases(makePhases(6));
      item.clearPhases();
      expect(item.getPhaseCount()).toBe(0);
    });

    it("reflects the most recent call to setPhases()", () => {
      item.setPhases(makePhases(10));
      item.setPhases(makePhases(2));
      expect(item.getPhaseCount()).toBe(2);
    });
  });
});
