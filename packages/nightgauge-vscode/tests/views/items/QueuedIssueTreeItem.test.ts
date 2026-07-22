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
