import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import { IssueTreeItem, type IssueInfo } from "../../../src/views/items/IssueTreeItem";

describe("IssueTreeItem", () => {
  describe("constructor", () => {
    it("should create tree item with correct label", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Change click behavior to open GitHub issue",
        branch: "feat/297-change-click-to-open-github-issue",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.label).toBe("#297 - Change click behavior to open GitHub issue");
    });

    it("should set collapsible state to Expanded", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
    });

    it("should set context value to issue", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.contextValue).toBe("issue");
    });

    it("should set issueNumber property", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.issueNumber).toBe(297);
    });

    it("should set description to branch name", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.description).toBe("feat/297-test");
    });
  });

  describe("command property (Issue #297)", () => {
    it("should set command to viewIssueOnGitHub when URL is provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.command).toBeDefined();
      expect(item.command?.command).toBe("nightgauge.viewIssueOnGitHub");
      expect(item.command?.title).toBe("View on GitHub");
      expect(item.command?.arguments).toEqual([item]);
    });

    it("should set issueUrl property when URL is provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.issueUrl).toBe("https://github.com/nightgauge/nightgauge/issues/297");
    });

    it("should not set command when URL is not provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.command).toBeUndefined();
      expect(item.issueUrl).toBeUndefined();
    });
  });

  describe("tooltip", () => {
    it("should include click hint when URL is provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.tooltip).toBeDefined();
      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Click to view on GitHub");
    });

    it("should not include click hint when URL is not provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      expect(item.tooltip).toBeDefined();
      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).not.toContain("Click to view on GitHub");
    });

    it("should include issue number, title, and branch", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Change click behavior",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Issue #297");
      expect(tooltip.value).toContain("Change click behavior");
      expect(tooltip.value).toContain("feat/297-test");
    });

    it("should include baseBranch if provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        baseBranch: "main",
      };

      const item = new IssueTreeItem(issueInfo);

      const tooltip = item.tooltip as vscode.MarkdownString;
      expect(tooltip.value).toContain("Target:");
      expect(tooltip.value).toContain("main");
    });

    it("should include labels if provided", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        labels: ["type:feature", "priority:high"],
      };

      const item = new IssueTreeItem(issueInfo);

      const tooltip = item.tooltip as vscode.MarkdownString;
      // priority: labels are shown as "Priority: P1 (High)", not in the raw Labels: list
      expect(tooltip.value).toContain("**Priority:** P1 (High)");
      expect(tooltip.value).toContain("Labels:");
      expect(tooltip.value).toContain("type:feature");
      // priority:high is filtered from the Labels: section (shown as Priority line above)
      expect(tooltip.value).not.toContain("`priority:high`");
    });
  });

  describe("getInfo", () => {
    it("should return a copy of issue info", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);
      const info = item.getInfo();

      expect(info).toEqual(issueInfo);
      expect(info).not.toBe(issueInfo); // Should be a copy
    });
  });

  describe("update", () => {
    it("should update title and label", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Original Title",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);
      item.update({ title: "Updated Title" });

      expect(item.label).toBe("#297 - Updated Title");
    });

    it("should update branch and description", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-original",
      };

      const item = new IssueTreeItem(issueInfo);
      item.update({ branch: "feat/297-updated" });

      expect(item.description).toBe("feat/297-updated");
    });

    it("should update baseBranch", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);
      item.update({ baseBranch: "develop" });

      const info = item.getInfo();
      expect(info.baseBranch).toBe("develop");
    });

    it("should update labels", () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
      };

      const item = new IssueTreeItem(issueInfo);
      item.update({ labels: ["new:label"] });

      const info = item.getInfo();
      expect(info.labels).toEqual(["new:label"]);
    });
  });
});
