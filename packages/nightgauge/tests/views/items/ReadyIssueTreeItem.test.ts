import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import { ReadyIssueTreeItem } from "../../../src/views/items/ReadyIssueTreeItem";
import { DependencySectionTreeItem } from "../../../src/views/items/DependencySectionTreeItem";
import {
  createMockReadyIssue,
  createMockBlockedIssue,
  createMockBlockingIssue,
} from "../../mocks/github-api";

describe("ReadyIssueTreeItem", () => {
  describe("constructor", () => {
    it("should create tree item with correct label", () => {
      const issue = createMockReadyIssue({
        number: 110,
        title: "Add feature",
      });

      const item = new ReadyIssueTreeItem(issue);

      expect(item.label).toBe("#110 - Add feature");
    });

    it("should set collapsible state to None for unblocked issues", () => {
      const issue = createMockReadyIssue({ blockedBy: undefined });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("should set collapsible state to Collapsed for blocked issues", () => {
      const issue = createMockBlockedIssue();
      const item = new ReadyIssueTreeItem(issue);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should set collapsible state to Collapsed for issues that block others", () => {
      const blockedIssue = createMockBlockingIssue({
        number: 130,
        state: "OPEN",
      });
      const issue = createMockReadyIssue({
        blockedBy: undefined,
        blocks: [blockedIssue],
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
    });

    it("should set context value to readyIssue", () => {
      const issue = createMockReadyIssue();
      const item = new ReadyIssueTreeItem(issue);

      expect(item.contextValue).toBe("readyIssue");
    });

    it("should set issueNumber and issueUrl properties", () => {
      const issue = createMockReadyIssue({
        number: 42,
        url: "https://github.com/org/repo/issues/42",
      });

      const item = new ReadyIssueTreeItem(issue);

      expect(item.issueNumber).toBe(42);
      expect(item.issueUrl).toBe("https://github.com/org/repo/issues/42");
    });
  });

  describe("icon based on blocking status and priority", () => {
    it("should show issues icon for unblocked issues without priority", () => {
      const issue = createMockReadyIssue({
        blockedBy: undefined,
        priority: null,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("issues");
    });

    it("should show lock icon for blocked issues (takes precedence over priority)", () => {
      const issue = createMockBlockedIssue();
      // Blocked issue has priority but lock should take precedence
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("lock");
    });

    it("should show circle-filled icon with error color for P0 (Critical)", () => {
      const issue = createMockReadyIssue({
        priority: "P0",
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("circle-filled");
      expect(icon.color).toBeDefined();
      expect((icon.color as vscode.ThemeColor).id).toBe("problemsErrorIcon.foreground");
    });

    it("should show circle-filled icon with warning color for P1 (High)", () => {
      const issue = createMockReadyIssue({
        priority: "P1",
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("circle-filled");
      expect(icon.color).toBeDefined();
      expect((icon.color as vscode.ThemeColor).id).toBe("problemsWarningIcon.foreground");
    });

    it("should show circle-filled icon with blue color for P2 (Medium/Low)", () => {
      const issue = createMockReadyIssue({
        priority: "P2",
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("circle-filled");
      expect(icon.color).toBeDefined();
      expect((icon.color as vscode.ThemeColor).id).toBe("charts.blue");
    });

    it("should show issues icon when priority is null and not blocked", () => {
      const issue = createMockReadyIssue({
        priority: null,
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("issues");
    });
  });

  describe("children for dependencies", () => {
    it("should have no children for unblocked issues", () => {
      const issue = createMockReadyIssue({ blockedBy: undefined });
      const item = new ReadyIssueTreeItem(issue);

      const children = item.getChildren();
      expect(children).toHaveLength(0);
    });

    it("should create DependencySectionTreeItem for blocked issues", () => {
      const blockingIssues = [
        createMockBlockingIssue({ number: 100, title: "Dependency 1" }),
        createMockBlockingIssue({ number: 101, title: "Dependency 2" }),
      ];
      const issue = createMockBlockedIssue(blockingIssues);
      const item = new ReadyIssueTreeItem(issue);

      const children = item.getChildren();
      expect(children).toHaveLength(1); // One section: "Blocked by"
      expect(children[0]).toBeInstanceOf(DependencySectionTreeItem);
      expect((children[0] as DependencySectionTreeItem).sectionType).toBe("blockedBy");
    });

    it("should create DependencySectionTreeItem for issues that block others", () => {
      const blockedIssue = createMockBlockingIssue({
        number: 130,
        state: "OPEN",
      });
      const issue = createMockReadyIssue({
        blockedBy: undefined,
        blocks: [blockedIssue],
      });
      const item = new ReadyIssueTreeItem(issue);

      const children = item.getChildren();
      expect(children).toHaveLength(1); // One section: "Blocks"
      expect(children[0]).toBeInstanceOf(DependencySectionTreeItem);
      expect((children[0] as DependencySectionTreeItem).sectionType).toBe("blocks");
    });

    it("should create both sections when issue has blockers and blocks others", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 100,
        title: "Blocker",
      });
      const blockedIssue = createMockBlockingIssue({
        number: 130,
        title: "Blocked",
        state: "OPEN",
      });
      const issue = createMockReadyIssue({
        blockedBy: [blockingIssue],
        blocks: [blockedIssue],
      });
      const item = new ReadyIssueTreeItem(issue);

      const children = item.getChildren();
      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(DependencySectionTreeItem);
      expect(children[1]).toBeInstanceOf(DependencySectionTreeItem);
      expect((children[0] as DependencySectionTreeItem).sectionType).toBe("blockedBy");
      expect((children[1] as DependencySectionTreeItem).sectionType).toBe("blocks");
    });

    it("should have no children when showDependencies is false", () => {
      const issue = createMockBlockedIssue();
      const item = new ReadyIssueTreeItem(issue, { showDependencies: false });

      const children = item.getChildren();
      expect(children).toHaveLength(0);
    });

    it("should set collapsible state to None when showDependencies is false", () => {
      const issue = createMockBlockedIssue();
      const item = new ReadyIssueTreeItem(issue, { showDependencies: false });

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });
  });

  describe("description with dependency count", () => {
    it("should show blocked count and size when issue has blockers", () => {
      // Issue #443: Updated format to show 🔒N blockers
      const blockingIssues = [
        createMockBlockingIssue({ number: 100 }),
        createMockBlockingIssue({ number: 101 }),
        createMockBlockingIssue({ number: 102 }),
      ];
      const issue = createMockReadyIssue({
        size: "M",
        blockedBy: blockingIssues,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.description).toBe("🔒3 blockers [M]");
    });

    it("should show only blocked count when size is null", () => {
      // Issue #443: Updated format to show 🔒N blocker(s)
      const blockingIssue = createMockBlockingIssue({ number: 100 });
      const issue = createMockReadyIssue({
        size: null,
        blockedBy: [blockingIssue],
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.description).toBe("🔒1 blocker");
    });

    it("should show size in bracketed format when both priority and size present", () => {
      const issue = createMockReadyIssue({
        priority: "P1",
        size: "M",
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      // Priority shown via icon, description shows only size
      expect(item.description).toBe("[M]");
    });

    it("should be empty when size is null and not blocked", () => {
      const issue = createMockReadyIssue({
        priority: "P1",
        size: null,
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.description).toBe("");
    });

    it("should show only size in brackets when priority is null and not blocked", () => {
      const issue = createMockReadyIssue({
        priority: null,
        size: "M",
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.description).toBe("[M]");
    });

    it("should be empty when both priority and size are null and not blocked", () => {
      const issue = createMockReadyIssue({
        priority: null,
        size: null,
        blockedBy: undefined,
      });
      const item = new ReadyIssueTreeItem(issue);

      expect(item.description).toBe("");
    });

    it("should show all size values in correct format", () => {
      const sizes = ["XS", "S", "M", "L", "XL"] as const;

      for (const size of sizes) {
        const issue = createMockReadyIssue({ size, blockedBy: undefined });
        const item = new ReadyIssueTreeItem(issue);
        expect(item.description).toBe(`[${size}]`);
      }
    });

    it("should not show blocked count when showDependencies is false", () => {
      const blockingIssue = createMockBlockingIssue({ number: 100 });
      const issue = createMockReadyIssue({
        size: "M",
        blockedBy: [blockingIssue],
      });
      const item = new ReadyIssueTreeItem(issue, { showDependencies: false });

      expect(item.description).toBe("[M]");
    });
  });

  describe("tooltip", () => {
    it("should include issue number and title", () => {
      const issue = createMockReadyIssue({
        number: 110,
        title: "Add feature",
      });
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("#110");
      expect(tooltip.value).toContain("Add feature");
    });

    it("should include priority when present", () => {
      const issue = createMockReadyIssue({ priority: "P0" });
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Priority:");
      expect(tooltip.value).toContain("Critical");
    });

    it("should include size when present", () => {
      const issue = createMockReadyIssue({ size: "L" });
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Size:");
      expect(tooltip.value).toContain("Large");
    });

    it("should include blocking dependencies section when blocked", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 100,
        title: "Foundation",
      });
      const issue = createMockBlockedIssue([blockingIssue]);
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Blocked By:");
      expect(tooltip.value).toContain("#100");
      expect(tooltip.value).toContain("Foundation");
    });

    it("should include blocks section when issue blocks others", () => {
      const blockedIssue = createMockBlockingIssue({
        number: 130,
        title: "Dependent feature",
        state: "OPEN",
      });
      const issue = createMockReadyIssue({
        blockedBy: undefined,
        blocks: [blockedIssue],
      });
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Blocks:");
      expect(tooltip.value).toContain("#130");
      expect(tooltip.value).toContain("Dependent feature");
    });

    it("should not include blocking section when unblocked", () => {
      const issue = createMockReadyIssue({ blockedBy: undefined });
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).not.toContain("Blocked By:");
    });

    it("should list multiple blocking issues", () => {
      const blockingIssues = [
        createMockBlockingIssue({ number: 100, title: "Dependency 1" }),
        createMockBlockingIssue({ number: 101, title: "Dependency 2" }),
      ];
      const issue = createMockBlockedIssue(blockingIssues);
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("#100");
      expect(tooltip.value).toContain("Dependency 1");
      expect(tooltip.value).toContain("#101");
      expect(tooltip.value).toContain("Dependency 2");
    });

    it("should be trusted markdown", () => {
      const issue = createMockReadyIssue();
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.isTrusted).toBe(true);
    });

    it("should not show dependencies in tooltip when showDependencies is false", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 200,
        title: "Blocker",
      });
      // Create custom issue without #200 in title
      const issue = createMockReadyIssue({
        number: 150,
        title: "Feature with dependency",
        blockedBy: [blockingIssue],
      });
      const item = new ReadyIssueTreeItem(issue, { showDependencies: false });

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).not.toContain("Blocked By:");
      expect(tooltip.value).not.toContain("#200"); // Blocker number should not appear
    });
  });

  describe("command property (Issue #297)", () => {
    it("should set command to viewIssueOnGitHub", () => {
      const issue = createMockReadyIssue({
        number: 297,
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      });

      const item = new ReadyIssueTreeItem(issue);

      expect(item.command).toBeDefined();
      expect(item.command?.command).toBe("nightgauge.viewIssueOnGitHub");
      expect(item.command?.title).toBe("View on GitHub");
      expect(item.command?.arguments).toEqual([item]);
    });

    it("should include drag hint in tooltip", () => {
      const issue = createMockReadyIssue();
      const item = new ReadyIssueTreeItem(issue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Click to view on GitHub");
      expect(tooltip.value).toContain("drag to add to pipeline");
    });
  });

  describe("getIssue", () => {
    it("should return a copy of the issue", () => {
      const issue = createMockReadyIssue();
      const item = new ReadyIssueTreeItem(issue);
      const retrieved = item.getIssue();

      expect(retrieved).toEqual(issue);
      expect(retrieved).not.toBe(issue); // Should be a copy
    });
  });
});
