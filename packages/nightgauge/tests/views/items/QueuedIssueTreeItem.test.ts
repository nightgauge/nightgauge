/**
 * QueuedIssueTreeItem Tests
 *
 * Tests for the queue tree item.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 * @see Issue #823 - Blocked Indicators on Queued Issues
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueuedIssueTreeItem } from "../../../src/views/items/QueuedIssueTreeItem";
import { createMockQueueItem } from "../../mocks/queue";
import type { BlockingIssue } from "../../../src/services/ProjectBoardService";

// Mock vscode
vi.mock("vscode", () => ({
  TreeItemCollapsibleState: {
    None: 0,
    Collapsed: 1,
    Expanded: 2,
  },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: any
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    constructor(public value: string) {}
    isTrusted: boolean = false;
    appendMarkdown(val: string) {
      this.value += val;
      return this;
    }
  },
  TreeItem: class {
    label: string = "";
    description: string = "";
    tooltip: any;
    iconPath: any;
    contextValue: string = "";
    collapsibleState: number = 0;
    command: any;
    accessibilityInformation: any;
    constructor(label: string, collapsibleState?: number) {
      this.label = label;
      this.collapsibleState = collapsibleState ?? 0;
    }
  },
}));

// Mock BaseTreeItem to extend vscode.TreeItem with setIconWithColor
vi.mock("../../../src/views/items/BaseTreeItem", async () => {
  const vscode = await import("vscode");
  return {
    BaseTreeItem: class extends vscode.TreeItem {
      getChildren() {
        return [];
      }
      protected setIcon(codicon: string): void {
        this.iconPath = new vscode.ThemeIcon(codicon);
      }
      protected setIconWithColor(codicon: string, color: any): void {
        this.iconPath = new vscode.ThemeIcon(codicon, color);
      }
    },
  };
});

describe("QueuedIssueTreeItem", () => {
  describe("single issue rendering", () => {
    it("should display issue number and title", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          issueNumber: 42,
          title: "Add dark mode",
          position: 1,
        })
      );

      expect(item.label).toBe("#42 - Add dark mode");
      expect(item.description).toBe("Position 1");
    });

    it("should use clock icon for pending items", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ status: "pending" }));

      expect((item.iconPath as any).id).toBe("clock");
    });

    it("should set contextValue with status", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ status: "pending" }));

      expect(item.contextValue).toBe("queuedIssue.pending");
    });
  });

  describe("blocked indicators (Issue #823)", () => {
    const openBlocker: BlockingIssue = {
      number: 100,
      title: "Prerequisite feature",
      url: "https://github.com/nightgauge/nightgauge/issues/100",
      state: "OPEN",
    };

    const closedBlocker: BlockingIssue = {
      number: 101,
      title: "Already resolved",
      url: "https://github.com/nightgauge/nightgauge/issues/101",
      state: "CLOSED",
    };

    it("should show lock icon for blocked issues", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ blockedBy: [openBlocker] }));

      expect((item.iconPath as any).id).toBe("lock");
      expect((item.iconPath as any).color?.id).toBe("problemsErrorIcon.foreground");
    });

    it("should add (blocked) suffix to label", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          issueNumber: 42,
          title: "Add dark mode",
          blockedBy: [openBlocker],
        })
      );

      expect(item.label).toBe("#42 - Add dark mode (blocked)");
    });

    it("should show blocker count in description", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          position: 3,
          blockedBy: [openBlocker],
        })
      );

      expect(item.description).toContain("🔒1 blocker");
      expect(item.description).toContain("Position 3");
    });

    it("should pluralize blocker count for multiple blockers", () => {
      const secondBlocker: BlockingIssue = {
        number: 200,
        title: "Another blocker",
        url: "https://github.com/nightgauge/nightgauge/issues/200",
        state: "OPEN",
      };

      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          blockedBy: [openBlocker, secondBlocker],
        })
      );

      expect(item.description).toContain("🔒2 blockers");
    });

    it("should show blocker details in tooltip", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ blockedBy: [openBlocker] }));

      const tooltipValue = (item.tooltip as any).value;
      expect(tooltipValue).toContain("🔒 Blocked By:");
      expect(tooltipValue).toContain("#100: Prerequisite feature");
      expect(tooltipValue).toContain("🔴");
    });

    it("should not show blocked indicators when blockedBy is empty", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          issueNumber: 42,
          title: "Add dark mode",
          blockedBy: [],
        })
      );

      expect(item.label).toBe("#42 - Add dark mode");
      expect((item.iconPath as any).id).toBe("clock");
      expect(item.description).toBe("Position 1");
    });

    it("should not show blocked indicators when only closed blockers", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          issueNumber: 42,
          title: "Add dark mode",
          blockedBy: [closedBlocker],
        })
      );

      expect(item.label).toBe("#42 - Add dark mode");
      expect((item.iconPath as any).id).toBe("clock");
    });

    it("should not show blocked indicators when blockedBy is undefined", () => {
      const item = new QueuedIssueTreeItem(
        createMockQueueItem({
          issueNumber: 42,
          title: "Add dark mode",
        })
      );

      expect(item.label).toBe("#42 - Add dark mode");
      expect((item.iconPath as any).id).toBe("clock");
    });

    it("should include blocked status in accessibility label", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ blockedBy: [openBlocker] }));

      const a11yLabel = item.accessibilityInformation?.label ?? "";
      expect(a11yLabel).toContain("Blocked by 1 issue.");
    });

    it("should update blocked indicators via update()", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ issueNumber: 42, position: 1 }));

      // Initially unblocked
      expect((item.iconPath as any).id).toBe("clock");

      // Update to blocked
      item.update(
        createMockQueueItem({
          issueNumber: 42,
          position: 1,
          blockedBy: [openBlocker],
        })
      );

      expect((item.iconPath as any).id).toBe("lock");
      expect(item.label).toBe("#42 - Test Issue (blocked)");
      expect(item.description).toContain("🔒1 blocker");
    });
  });

  /**
   * Issue #881 — the tooltip promised a `baseline-defer-sweep` cron that has
   * never existed in `.github/workflows/`, so the operator waited for an
   * automation that was not coming. #881 replaced the promise with the truth
   * at the time: the item does not resume, run the verb.
   *
   * Issue #885 then MADE it resume — the autonomous daemon now sweeps these
   * items, which is where the trigger always had to live (the queue is local
   * and gitignored, so a CI cron has no queue to promote). The tooltip must
   * now say THAT, and must still not resurrect the cron: an automation that
   * exists and an automation that never did are different claims, and the
   * only wrong answer is a tooltip describing whichever one is not true today.
   */
  describe("baseline-CI paused tooltip (Issues #881, #885)", () => {
    const baselinePausedItem = () =>
      createMockQueueItem({
        issueNumber: 881,
        position: 1,
        status: "paused",
        pausedReason: {
          kind: "baseline_ci_red",
          workflow: "ci.yml",
          job: "Integration & E2E Tests",
          failed_runs: 3,
          lookback_runs: 5,
        },
      });

    it("must not promise a cron that does not exist", () => {
      const tooltip = (new QueuedIssueTreeItem(baselinePausedItem()).tooltip as any)
        .value as string;

      expect(tooltip).not.toContain("baseline-defer-sweep");
      expect(tooltip).not.toMatch(/auto-resumes/i);
      expect(tooltip).not.toMatch(/\bcron\b/i);
    });

    it("says the item resumes automatically, and still offers the manual verb", () => {
      const tooltip = (new QueuedIssueTreeItem(baselinePausedItem()).tooltip as any)
        .value as string;

      // #885: the daemon sweeps and resumes it.
      expect(tooltip).toMatch(/autonomous daemon/i);
      expect(tooltip).toMatch(/resumes/i);
      // The stale #881 claim must be gone — it is now false.
      expect(tooltip).not.toMatch(/does \*\*not\*\* resume on its own/i);
      // The verb still releases it immediately, and the operator should know.
      expect(tooltip).toContain("nightgauge baseline-gate promote");
      // The evidence that made the deferral legible must survive the rewording.
      expect(tooltip).toContain("ci.yml");
      expect(tooltip).toContain("3/5 recent runs");
    });
  });

  /**
   * Issue #1146 — the Go scheduler gained an `excluded_label` paused reason
   * when `exclude_labels` moved to the dequeue chokepoint. The TypeScript
   * union and the label function knew only the three older kinds, so the
   * generic fallback rendered the raw discriminant: an operator saw
   * `excluded_label` where every other pause reason reads as a phrase, and the
   * label they need to remove was never named.
   */
  describe("human-only label paused rendering (Issue #1146)", () => {
    const excludedPausedItem = (label?: string) =>
      createMockQueueItem({
        issueNumber: 1146,
        position: 1,
        status: "paused",
        pausedReason: {
          kind: "excluded_label",
          label,
          summary: label
            ? `carries human-only label "${label}" (autonomous.exclude_labels)`
            : undefined,
        },
      });

    it("renders the description as a phrase naming the label, not the raw discriminant", () => {
      const item = new QueuedIssueTreeItem(excludedPausedItem("owner-action"));

      const description = item.description as string;
      expect(description).toContain("paused: human-only label owner-action");
      expect(description).not.toContain("excluded_label");
    });

    it("reads as prose in the accessibility label, not the raw discriminant", () => {
      const item = new QueuedIssueTreeItem(excludedPausedItem("owner-action"));

      const a11yLabel = (item.accessibilityInformation?.label ?? "") as string;
      expect(a11yLabel).toContain("Paused due to the human-only label owner-action");
      expect(a11yLabel).not.toContain("excluded_label");
      // Nothing sweeps these — the operator removes the label or discards.
      expect(a11yLabel).toContain("Resumes only via operator action.");
    });

    it("explains the hold in the tooltip and says what releases it", () => {
      const tooltip = (new QueuedIssueTreeItem(excludedPausedItem("owner-action")).tooltip as any)
        .value as string;

      expect(tooltip).toContain("Reason: human-only label");
      expect(tooltip).toContain("owner-action");
      expect(tooltip).toContain("autonomous.exclude_labels");
      expect(tooltip).toMatch(/remove the label/i);
      expect(tooltip).not.toContain("Reason: excluded_label");
      // It is not on any promote sweep — do not imply one.
      expect(tooltip).not.toMatch(/auto-resumes/i);
    });

    it("degrades to a phrase when the label is absent", () => {
      const item = new QueuedIssueTreeItem(excludedPausedItem(undefined));

      const description = item.description as string;
      const a11yLabel = (item.accessibilityInformation?.label ?? "") as string;
      expect(description).toContain("paused: human-only label");
      expect(description).not.toContain("excluded_label");
      expect(a11yLabel).toContain("a human-only label");
      expect(a11yLabel).not.toContain("excluded_label");
    });
  });

  describe("update method", () => {
    it("should update single item correctly", () => {
      const item = new QueuedIssueTreeItem(createMockQueueItem({ issueNumber: 42, position: 1 }));

      item.update(
        createMockQueueItem({
          issueNumber: 42,
          title: "Updated Title",
          position: 2,
          status: "processing",
        })
      );

      expect(item.label).toBe("#42 - Updated Title");
      expect(item.description).toBe("Position 2");
      expect(item.contextValue).toBe("queuedIssue.processing");
    });
  });
});
