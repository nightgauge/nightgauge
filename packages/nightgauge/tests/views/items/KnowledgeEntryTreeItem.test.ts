import { describe, it, expect } from "vitest";
import { KnowledgeEntryTreeItem } from "../../../src/views/items/KnowledgeEntryTreeItem";
import type { KnowledgeListEntry } from "@nightgauge/sdk";
import * as vscode from "vscode";

function makeListEntry(overrides: Partial<KnowledgeListEntry> = {}): KnowledgeListEntry {
  return {
    filePath: "/workspace/.nightgauge/knowledge/epics/1686-knowledge/PRD.md",
    relativePath: ".nightgauge/knowledge/epics/1686-knowledge/PRD.md",
    entry: {
      title: "Knowledge Tree Provider",
      type: "prd",
      created: "2026-03-09T00:00:00Z",
      updated: "2026-03-09T00:00:00Z",
    },
    ...overrides,
  };
}

describe("KnowledgeEntryTreeItem", () => {
  it("should use entry title as label when present", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.label).toBe("Knowledge Tree Provider");
  });

  it("should fall back to filename stem when title absent", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry({ entry: null }));
    expect(item.label).toBe("PRD");
  });

  it("should show issue number in description from path", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.description).toBe("#1686");
  });

  it("should not set description when path has no issue number", () => {
    const item = new KnowledgeEntryTreeItem(
      makeListEntry({
        relativePath: ".nightgauge/knowledge/glossary/terms.md",
      })
    );
    expect(item.description).toBeUndefined();
  });

  it("should set tooltip to relativePath", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.tooltip).toBe(".nightgauge/knowledge/epics/1686-knowledge/PRD.md");
  });

  it("should use file-text icon for prd type", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("file-text");
  });

  it("should use lightbulb icon for decision type", () => {
    const item = new KnowledgeEntryTreeItem(
      makeListEntry({
        entry: {
          title: "Decision",
          type: "decision",
          created: "2026-03-09T00:00:00Z",
          updated: "2026-03-09T00:00:00Z",
        },
      })
    );
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("lightbulb");
  });

  it("should use lightbulb icon for adr type", () => {
    const item = new KnowledgeEntryTreeItem(
      makeListEntry({
        entry: {
          title: "ADR",
          type: "adr",
          created: "2026-03-09T00:00:00Z",
          updated: "2026-03-09T00:00:00Z",
        },
      })
    );
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("lightbulb");
  });

  it("should use note icon for note type", () => {
    const item = new KnowledgeEntryTreeItem(
      makeListEntry({
        entry: {
          title: "Note",
          type: "note",
          created: "2026-03-09T00:00:00Z",
          updated: "2026-03-09T00:00:00Z",
        },
      })
    );
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("note");
  });

  it("should use file icon when entry frontmatter is null (no frontmatter)", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry({ entry: null }));
    expect((item.iconPath as vscode.ThemeIcon).id).toBe("file");
    expect(item.contextValue).toBe("knowledgeEntry");
  });

  it("should set contextValue to knowledgeEntry", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.contextValue).toBe("knowledgeEntry");
  });

  it("should set command to vscode.open with file URI", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.command).toBeDefined();
    expect(item.command!.command).toBe("vscode.open");
    expect(item.command!.arguments).toHaveLength(1);
    expect(item.command!.arguments![0].fsPath).toBe(
      "/workspace/.nightgauge/knowledge/epics/1686-knowledge/PRD.md"
    );
  });

  it("should be non-collapsible (leaf node)", () => {
    const item = new KnowledgeEntryTreeItem(makeListEntry());
    expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.None);
  });
});
