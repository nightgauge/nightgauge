import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import { PhaseTreeItem, type PhaseStatus } from "../../../src/views/items/PhaseTreeItem";

describe("PhaseTreeItem", () => {
  describe("constructor", () => {
    it("should default to pending status when no status provided", () => {
      const item = new PhaseTreeItem("load-context");

      expect(item.getStatus()).toBe("pending");
    });

    it("should set phaseName to the original kebab-case name", () => {
      const item = new PhaseTreeItem("load-context");

      expect(item.phaseName).toBe("load-context");
    });

    it("should always have TreeItemCollapsibleState.None", () => {
      const item = new PhaseTreeItem("load-context");

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });

    it("should accept an explicit status in the constructor", () => {
      const item = new PhaseTreeItem("load-context", "running");

      expect(item.getStatus()).toBe("running");
    });
  });

  describe("label formatting (kebab-case to Title Case)", () => {
    it("should convert a single-word name to Title Case", () => {
      const item = new PhaseTreeItem("plan");

      expect(item.label).toBe("Plan");
    });

    it("should convert load-context to Title Case", () => {
      const item = new PhaseTreeItem("load-context");

      expect(item.label).toBe("Load Context");
    });

    it("should convert read-planning-context to Title Case", () => {
      const item = new PhaseTreeItem("read-planning-context");

      expect(item.label).toBe("Read Planning Context");
    });

    it("should convert analyze-changes to Title Case", () => {
      const item = new PhaseTreeItem("analyze-changes");

      expect(item.label).toBe("Analyze Changes");
    });

    it("should convert run-tests to Title Case", () => {
      const item = new PhaseTreeItem("run-tests");

      expect(item.label).toBe("Run Tests");
    });

    it("should convert create-pull-request to Title Case", () => {
      const item = new PhaseTreeItem("create-pull-request");

      expect(item.label).toBe("Create Pull Request");
    });

    it("should preserve label regardless of status", () => {
      const statuses: PhaseStatus[] = ["pending", "running", "complete", "skipped"];

      for (const status of statuses) {
        const item = new PhaseTreeItem("load-context", status);
        expect(item.label).toBe("Load Context");
      }
    });
  });

  describe("icon mapping per status", () => {
    it("should use circle-outline icon for pending status", () => {
      const item = new PhaseTreeItem("load-context", "pending");

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("circle-outline");
    });

    it("should use sync~spin icon for running status", () => {
      const item = new PhaseTreeItem("load-context", "running");

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("sync~spin");
    });

    it("should use check icon for complete status", () => {
      const item = new PhaseTreeItem("load-context", "complete");

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("check");
    });

    it("should apply testing.iconPassed color for complete status", () => {
      const item = new PhaseTreeItem("load-context", "complete");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.color).toBeDefined();
      expect((icon.color as vscode.ThemeColor).id).toBe("testing.iconPassed");
    });

    it("should use debug-step-over icon for skipped status", () => {
      const item = new PhaseTreeItem("load-context", "skipped");

      expect(item.iconPath).toBeDefined();
      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("debug-step-over");
    });

    it("should not set a color for pending status", () => {
      const item = new PhaseTreeItem("load-context", "pending");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.color).toBeUndefined();
    });

    it("should not set a color for running status", () => {
      const item = new PhaseTreeItem("load-context", "running");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.color).toBeUndefined();
    });

    it("should not set a color for skipped status", () => {
      const item = new PhaseTreeItem("load-context", "skipped");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.color).toBeUndefined();
    });
  });

  describe("contextValue per status", () => {
    it("should set contextValue to phase-pending for pending status", () => {
      const item = new PhaseTreeItem("load-context", "pending");

      expect(item.contextValue).toBe("phase-pending");
    });

    it("should set contextValue to phase-running for running status", () => {
      const item = new PhaseTreeItem("load-context", "running");

      expect(item.contextValue).toBe("phase-running");
    });

    it("should set contextValue to phase-complete for complete status", () => {
      const item = new PhaseTreeItem("load-context", "complete");

      expect(item.contextValue).toBe("phase-complete");
    });

    it("should set contextValue to phase-skipped for skipped status", () => {
      const item = new PhaseTreeItem("load-context", "skipped");

      expect(item.contextValue).toBe("phase-skipped");
    });
  });

  describe("description per status", () => {
    it('should show "pending" as description for pending status', () => {
      const item = new PhaseTreeItem("load-context", "pending");

      expect(item.description).toBe("pending");
    });

    it("should show empty string as description for running status", () => {
      const item = new PhaseTreeItem("load-context", "running");

      expect(item.description).toBe("");
    });

    it('should show "complete" as description for complete status', () => {
      const item = new PhaseTreeItem("load-context", "complete");

      expect(item.description).toBe("complete");
    });

    it('should show "skipped" as description for skipped status', () => {
      const item = new PhaseTreeItem("load-context", "skipped");

      expect(item.description).toBe("skipped");
    });
  });

  describe("setStatus()", () => {
    it("should update status via getStatus()", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("running");

      expect(item.getStatus()).toBe("running");
    });

    it("should update icon when status changes from pending to running", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("running");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("sync~spin");
    });

    it("should update icon when status changes from running to complete", () => {
      const item = new PhaseTreeItem("load-context", "running");
      item.setStatus("complete");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("check");
    });

    it("should apply color when transitioning to complete", () => {
      const item = new PhaseTreeItem("load-context", "running");
      item.setStatus("complete");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect((icon.color as vscode.ThemeColor).id).toBe("testing.iconPassed");
    });

    it("should update icon when status changes to skipped", () => {
      const item = new PhaseTreeItem("load-context", "running");
      item.setStatus("skipped");

      const icon = item.iconPath as vscode.ThemeIcon;
      expect(icon.id).toBe("debug-step-over");
    });

    it("should update contextValue when status changes", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("complete");

      expect(item.contextValue).toBe("phase-complete");
    });

    it("should clear description when transitioning to running", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("running");

      expect(item.description).toBe("");
    });

    it("should show status text in description when transitioning away from running", () => {
      const item = new PhaseTreeItem("load-context", "running");
      item.setStatus("complete");

      expect(item.description).toBe("complete");
    });

    it("should not change the label when status changes", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("complete");

      expect(item.label).toBe("Load Context");
    });

    it("should not change phaseName when status changes", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("complete");

      expect(item.phaseName).toBe("load-context");
    });

    it("should not change collapsibleState when status changes", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("complete");

      expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
    });
  });

  describe("getStatus()", () => {
    it("should return the current status", () => {
      const item = new PhaseTreeItem("load-context", "complete");

      expect(item.getStatus()).toBe("complete");
    });

    it("should reflect the latest status after multiple setStatus calls", () => {
      const item = new PhaseTreeItem("load-context", "pending");
      item.setStatus("running");
      item.setStatus("complete");

      expect(item.getStatus()).toBe("complete");
    });
  });

  describe("collapsible state", () => {
    it("should always be None regardless of initial status", () => {
      const statuses: PhaseStatus[] = ["pending", "running", "complete", "skipped"];

      for (const status of statuses) {
        const item = new PhaseTreeItem("load-context", status);
        expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
      }
    });

    it("should have no children", () => {
      const item = new PhaseTreeItem("load-context", "pending");

      expect(item.getChildren()).toHaveLength(0);
    });
  });
});
