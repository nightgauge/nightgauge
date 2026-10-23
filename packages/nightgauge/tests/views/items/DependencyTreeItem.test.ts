import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import {
  DependencyTreeItem,
  type BlockingIssue,
} from "../../../src/views/items/DependencyTreeItem";
import { createMockBlockingIssue } from "../../mocks/github-api";

describe("DependencyTreeItem", () => {
  describe("constructor", () => {
    it("should create tree item with correct label", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 100,
        title: "Foundation feature",
      });

      const item = new DependencyTreeItem(blockingIssue);

      expect(item.label).toBe("#100 - Foundation feature");
    });

    it("should set collapsible state to None", () => {
      const blockingIssue = createMockBlockingIssue();
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("should set context value to dependency", () => {
      const blockingIssue = createMockBlockingIssue();
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.contextValue).toBe("dependency");
    });

    it("should set issueNumber and issueUrl properties", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 42,
        url: "https://github.com/org/repo/issues/42",
      });

      const item = new DependencyTreeItem(blockingIssue);

      expect(item.issueNumber).toBe(42);
      expect(item.issueUrl).toBe("https://github.com/org/repo/issues/42");
    });

    it("should set click command to viewIssueOnGitHub", () => {
      const blockingIssue = createMockBlockingIssue();
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.command).toBeDefined();
      expect(item.command?.command).toBe("nightgauge.viewIssueOnGitHub");
      expect(item.command?.arguments).toEqual([item]);
    });
  });

  describe("icon and state", () => {
    it("should show red circle icon for OPEN issues", () => {
      const blockingIssue = createMockBlockingIssue({ state: "OPEN" });
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("circle-filled");
    });

    it("should show green checkmark icon for CLOSED issues", () => {
      const blockingIssue = createMockBlockingIssue({ state: "CLOSED" });
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("pass");
    });

    it('should set description to "Open" for OPEN issues', () => {
      const blockingIssue = createMockBlockingIssue({ state: "OPEN" });
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.description).toBe("Open");
    });

    it('should set description to "Closed" for CLOSED issues', () => {
      const blockingIssue = createMockBlockingIssue({ state: "CLOSED" });
      const item = new DependencyTreeItem(blockingIssue);

      expect(item.description).toBe("Closed");
    });
  });

  describe("tooltip", () => {
    it("should include issue number and title", () => {
      const blockingIssue = createMockBlockingIssue({
        number: 100,
        title: "Foundation feature",
      });
      const item = new DependencyTreeItem(blockingIssue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("#100");
      expect(tooltip.value).toContain("Foundation feature");
    });

    it("should show warning message for OPEN issues", () => {
      const blockingIssue = createMockBlockingIssue({ state: "OPEN" });
      const item = new DependencyTreeItem(blockingIssue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("must be completed before work can begin");
    });

    it("should show success message for CLOSED issues", () => {
      const blockingIssue = createMockBlockingIssue({ state: "CLOSED" });
      const item = new DependencyTreeItem(blockingIssue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("no longer blocks work");
    });

    it("should be trusted markdown", () => {
      const blockingIssue = createMockBlockingIssue();
      const item = new DependencyTreeItem(blockingIssue);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.isTrusted).toBe(true);
    });
  });

  describe("getBlockingIssue", () => {
    it("should return a copy of the blocking issue", () => {
      const blockingIssue: BlockingIssue = {
        number: 100,
        title: "Foundation",
        url: "https://github.com/org/repo/issues/100",
        state: "OPEN",
      };

      const item = new DependencyTreeItem(blockingIssue);
      const retrieved = item.getBlockingIssue();

      expect(retrieved).toEqual(blockingIssue);
      expect(retrieved).not.toBe(blockingIssue); // Should be a copy
    });
  });
});
