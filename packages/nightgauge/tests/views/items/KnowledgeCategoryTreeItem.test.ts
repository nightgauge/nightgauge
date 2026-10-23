import { describe, it, expect } from "vitest";
import { KnowledgeCategoryTreeItem } from "../../../src/views/items/KnowledgeCategoryTreeItem";
import * as vscode from "vscode";

describe("KnowledgeCategoryTreeItem", () => {
  it("should set label and categoryKey", () => {
    const item = new KnowledgeCategoryTreeItem("Epics", "epics", 3);
    expect(item.label).toBe("Epics");
    expect(item.categoryKey).toBe("epics");
  });

  it("should show count in description when entries exist", () => {
    const item = new KnowledgeCategoryTreeItem("Features", "features", 5);
    expect(item.description).toBe("(5)");
  });

  it("should show empty description when no entries", () => {
    const item = new KnowledgeCategoryTreeItem("Glossary", "glossary", 0);
    expect(item.description).toBe("");
  });

  it("should use folder-opened icon when entries exist", () => {
    const item = new KnowledgeCategoryTreeItem("Epics", "epics", 2);
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("folder-opened");
  });

  it("should use folder icon when no entries", () => {
    const item = new KnowledgeCategoryTreeItem("Glossary", "glossary", 0);
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("folder");
  });

  it("should default to Collapsed state", () => {
    const item = new KnowledgeCategoryTreeItem("Epics", "epics", 1);
    expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
  });

  it("should accept custom collapsibleState", () => {
    const item = new KnowledgeCategoryTreeItem(
      "Epics",
      "epics",
      1,
      vscode.TreeItemCollapsibleState.Expanded
    );
    expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
  });

  it("should set contextValue to knowledgeCategory", () => {
    const item = new KnowledgeCategoryTreeItem("Epics", "epics", 0);
    expect(item.contextValue).toBe("knowledgeCategory");
  });
});
