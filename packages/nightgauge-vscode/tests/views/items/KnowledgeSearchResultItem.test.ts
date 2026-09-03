/**
 * Related Decisions rendered rows that read `decisions 0.69` (#1207).
 *
 * The label fell back to the file's basename whenever the snippet was empty,
 * and every knowledge base names its files the same two things — so every row
 * read `PRD` or `decisions`. The description was the raw BM25 score with
 * nothing saying what the number was.
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: vi.fn(function (this: Record<string, unknown>, label: string, state: number) {
    this.label = label;
    this.collapsibleState = state;
  }),
  ThemeIcon: vi.fn(function (this: Record<string, unknown>, id: string) {
    this.id = id;
  }),
  ThemeColor: vi.fn(function (this: Record<string, unknown>, id: string) {
    this.id = id;
  }),
  MarkdownString: vi.fn(function (this: Record<string, unknown>, value?: string) {
    this.value = value ?? "";
  }),
  Uri: { file: (p: string) => ({ fsPath: p }) },
}));

import {
  KnowledgeSearchResultItem,
  knowledgeTopicOf,
} from "../../../src/views/items/KnowledgeSearchResultItem";
import type { KnowledgeRecallHit } from "../../../src/services/IpcClientBase";

const HIT: KnowledgeRecallHit = {
  rank: 1,
  score: 0.69,
  path: ".nightgauge/knowledge/features/390-cost-cache-token-counts/decisions.md",
  kind: "issue",
  issue_number: 390,
  snippet: "Decision: route counts through the Go native counter.",
  stale: false,
  lifecycle_multiplier: 1,
};

describe("KnowledgeSearchResultItem (#1207)", () => {
  it("names the knowledge base, not the file", () => {
    const item = new KnowledgeSearchResultItem(HIT, "/workspace");
    expect(item.label).toBe("#390 cost cache token counts");
    // `decisions` is the one part of that path carrying no information: every
    // knowledge base has a file by that name.
    expect(item.label).not.toBe("decisions");
  });

  it("describes the row with the matching line, not a bare score", () => {
    const item = new KnowledgeSearchResultItem(HIT, "/workspace");
    expect(item.description).toBe("Decision: route counts through the Go native counter.");
    expect(item.description).not.toBe("0.69");
    expect(item.description).not.toContain("0.69");
  });

  it("keeps the score in the tooltip, labelled", () => {
    const item = new KnowledgeSearchResultItem(HIT, "/workspace");
    const tooltip = (item.tooltip as unknown as { value: string }).value;
    // A number the reader cannot interpret is not information. In the tooltip
    // it is at least named.
    expect(tooltip).toContain("BM25 relevance 0.69");
    expect(tooltip).toContain("cost cache token counts");
    expect(tooltip).toContain(HIT.path);
  });

  it("falls back to the filename, never to the score, when there is no snippet", () => {
    const item = new KnowledgeSearchResultItem({ ...HIT, snippet: "" }, "/workspace");
    expect(item.description).toBe("decisions.md");
    expect(item.description).not.toContain("0.69");
    expect(item.label).toBe("#390 cost cache token counts");
  });

  it("omits the issue prefix for a hit that is not issue-scoped", () => {
    const item = new KnowledgeSearchResultItem(
      {
        ...HIT,
        path: ".nightgauge/knowledge/workspace/architecture/forge-abstraction.md",
        issue_number: 0,
      },
      "/workspace"
    );
    expect(item.label).toBe("forge abstraction");
  });

  it("resolves a relative hit path against the workspace root", () => {
    const item = new KnowledgeSearchResultItem(HIT, "/workspace");
    expect(item.absolutePath).toBe(`/workspace/${HIT.path}`);
  });

  it("leaves an absolute hit path alone", () => {
    const abs = "/elsewhere/knowledge/features/390-x/decisions.md";
    const item = new KnowledgeSearchResultItem({ ...HIT, path: abs }, "/workspace");
    expect(item.absolutePath).toBe(abs);
  });

  it("truncates a long snippet rather than letting it fill the sidebar", () => {
    const item = new KnowledgeSearchResultItem({ ...HIT, snippet: "x".repeat(300) }, "/workspace");
    expect((item.description as string).length).toBeLessThanOrEqual(100);
  });
});

describe("knowledgeTopicOf", () => {
  it("strips the issue prefix and spaces the slug", () => {
    expect(knowledgeTopicOf("features/390-cost-cache-token-counts/decisions.md")).toBe(
      "cost cache token counts"
    );
  });

  it("uses the filename when a document has no topic directory", () => {
    expect(knowledgeTopicOf("forge-abstraction.md")).toBe("forge abstraction");
  });

  it("keeps a slug that is only digits from collapsing to nothing", () => {
    // `390-` strips to "", which must not produce an empty label.
    expect(knowledgeTopicOf("features/390-/decisions.md")).toBe("decisions");
  });
});
