/**
 * KnowledgeTreeProvider tests — rewired three-section model (#2964).
 *
 * The provider now needs (workspaceRoot, PipelineStateService, IpcClient).
 * Tests assert section structure, active-issue resolution from issue-{N}.json,
 * highlighting from planning-{N}.json.knowledge_read, related-decisions
 * routing through IPC, and the search results path.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as path from "node:path";
import { KnowledgeTreeProvider } from "../../src/views/KnowledgeTreeProvider";
import { KnowledgeSectionItem } from "../../src/views/items/KnowledgeSectionItem";
import { KnowledgeActiveFileItem } from "../../src/views/items/KnowledgeActiveFileItem";
import { KnowledgeSearchResultItem } from "../../src/views/items/KnowledgeSearchResultItem";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { IpcClient } from "../../src/services/IpcClient";
import type { KnowledgeRecallHit } from "../../src/services/IpcClientBase";

vi.mock("vscode", () => {
  const EventEmitter = vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  });
  return {
    EventEmitter,
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: vi.fn(function (label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }),
    ThemeIcon: vi.fn(function (id: string, color?: any) {
      this.id = id;
      this.color = color;
    }),
    ThemeColor: vi.fn(function (id: string) {
      this.id = id;
    }),
    Uri: { file: (p: string) => ({ fsPath: p }) },
    workspace: {
      createFileSystemWatcher: vi.fn(() => ({
        onDidCreate: vi.fn(),
        onDidChange: vi.fn(),
        onDidDelete: vi.fn(),
        dispose: vi.fn(),
      })),
    },
    RelativePattern: vi.fn(function (base: string, pattern: string) {
      this.base = base;
      this.pattern = pattern;
    }),
    commands: { executeCommand: vi.fn() },
  };
});

vi.mock("node:fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

import * as fsModule from "node:fs";

function makePipelineState(issueNumber: number | null): PipelineStateService {
  return {
    getActiveIssueBlockingPickup: vi.fn(() => issueNumber),
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
  } as unknown as PipelineStateService;
}

function makeIpcClient(overrides: Partial<IpcClient> = {}): IpcClient {
  return {
    knowledgeRelatedToIssue: vi.fn(async () => ({ hits: [] })),
    knowledgeSearch: vi.fn(async () => ({ hits: [], total_hits: 0 })),
    ...overrides,
  } as unknown as IpcClient;
}

const KB_PATH = "/workspace/.nightgauge/knowledge/features/42-my-feature";

function stubFs(opts: {
  knowledgePath?: string | null;
  knowledgeRead?: string[];
  existingFiles?: string[];
}): void {
  vi.mocked(fsModule.readFileSync).mockImplementation((p: unknown) => {
    const filePath = p as string;
    if (filePath.endsWith("issue-42.json")) {
      return JSON.stringify({ knowledge_path: opts.knowledgePath ?? null });
    }
    if (filePath.endsWith("planning-42.json")) {
      return JSON.stringify({ knowledge_read: opts.knowledgeRead ?? [] });
    }
    throw new Error(`ENOENT: ${filePath}`);
  });
  vi.mocked(fsModule.existsSync).mockImplementation((p: unknown) => {
    const fp = p as string;
    if (!opts.existingFiles) return true;
    return opts.existingFiles.some((f) => fp.endsWith(f));
  });
}

describe("KnowledgeTreeProvider (three-section model #2964)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns three section headers at the root", async () => {
    stubFs({ knowledgePath: null });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    expect(root).toHaveLength(3);
    expect(root[0]).toBeInstanceOf(KnowledgeSectionItem);
    expect((root[0] as KnowledgeSectionItem).sectionKind).toBe("active-issue");
    expect((root[1] as KnowledgeSectionItem).sectionKind).toBe("related");
    expect((root[2] as KnowledgeSectionItem).sectionKind).toBe("search");
    provider.dispose();
  });

  it("labels the Active Issue header with the current issue number", async () => {
    stubFs({ knowledgePath: null });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    expect(root[0].label).toBe("Active Issue (#42)");
    provider.dispose();
  });

  it("shows empty-state child when no active issue", async () => {
    stubFs({ knowledgePath: null });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(null),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const activeChildren = await provider.getChildren(root[0]);
    expect(activeChildren).toHaveLength(1);
    expect(activeChildren[0].label).toContain("No active issue");
    provider.dispose();
  });

  it("returns Active Issue file leaves for existing PRD/decisions", async () => {
    stubFs({
      knowledgePath: KB_PATH,
      knowledgeRead: [],
      existingFiles: ["PRD.md", "decisions.md"],
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const activeChildren = await provider.getChildren(root[0]);
    expect(activeChildren).toHaveLength(2);
    expect(activeChildren[0]).toBeInstanceOf(KnowledgeActiveFileItem);
    expect((activeChildren[0] as KnowledgeActiveFileItem).filePath).toBe(
      path.join(KB_PATH, "PRD.md")
    );
    expect((activeChildren[0] as KnowledgeActiveFileItem).highlighted).toBe(false);
    provider.dispose();
  });

  it("highlights files listed in planning.knowledge_read", async () => {
    stubFs({
      knowledgePath: KB_PATH,
      knowledgeRead: ["PRD.md"],
      existingFiles: ["PRD.md", "decisions.md"],
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const activeChildren = await provider.getChildren(root[0]);
    const prd = activeChildren.find((c) =>
      (c as KnowledgeActiveFileItem).filePath?.endsWith("PRD.md")
    ) as KnowledgeActiveFileItem;
    const decisions = activeChildren.find((c) =>
      (c as KnowledgeActiveFileItem).filePath?.endsWith("decisions.md")
    ) as KnowledgeActiveFileItem;
    expect(prd.highlighted).toBe(true);
    expect(decisions.highlighted).toBe(false);
    provider.dispose();
  });

  it("routes Related Decisions through knowledge.relatedToIssue IPC", async () => {
    stubFs({ knowledgePath: KB_PATH });
    const hits: KnowledgeRecallHit[] = [
      { rank: 1, score: 2.5, path: "a/b.md", kind: "issue", snippet: "Use BM25" },
    ];
    const ipc = makeIpcClient({
      knowledgeRelatedToIssue: vi.fn(async () => ({ hits })),
    });
    const provider = new KnowledgeTreeProvider("/workspace", makePipelineState(42), ipc);
    const root = await provider.getChildren();
    const relatedChildren = await provider.getChildren(root[1]);
    expect(ipc.knowledgeRelatedToIssue).toHaveBeenCalledWith(42, 10);
    expect(relatedChildren).toHaveLength(1);
    expect(relatedChildren[0]).toBeInstanceOf(KnowledgeSearchResultItem);
    provider.dispose();
  });

  it("emits empty-state when Related IPC returns no hits", async () => {
    stubFs({ knowledgePath: KB_PATH });
    const ipc = makeIpcClient();
    const provider = new KnowledgeTreeProvider("/workspace", makePipelineState(42), ipc);
    const root = await provider.getChildren();
    const relatedChildren = await provider.getChildren(root[1]);
    expect(relatedChildren).toHaveLength(1);
    expect(relatedChildren[0].label).toContain("No related decisions");
    provider.dispose();
  });

  it("Search section is empty until setSearchResults() is called", async () => {
    stubFs({ knowledgePath: KB_PATH });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    let root = await provider.getChildren();
    let searchChildren = await provider.getChildren(root[2]);
    expect(searchChildren).toHaveLength(1);
    expect(searchChildren[0].label).toContain("Search Knowledge");

    provider.setSearchResults([
      { rank: 1, score: 1.0, path: "x.md", kind: "issue", snippet: "match" },
      { rank: 2, score: 0.9, path: "y.md", kind: "issue", snippet: "another" },
    ]);
    root = await provider.getChildren();
    searchChildren = await provider.getChildren(root[2]);
    expect(searchChildren).toHaveLength(2);
    expect(searchChildren[0]).toBeInstanceOf(KnowledgeSearchResultItem);
    provider.dispose();
  });

  it("clearSearchResults empties the Search section", async () => {
    stubFs({ knowledgePath: KB_PATH });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    provider.setSearchResults([
      { rank: 1, score: 1.0, path: "x.md", kind: "issue", snippet: "match" },
    ]);
    provider.clearSearchResults();
    const root = await provider.getChildren();
    const searchChildren = await provider.getChildren(root[2]);
    expect(searchChildren).toHaveLength(1);
    expect(searchChildren[0].label).toContain("Search Knowledge");
    provider.dispose();
  });

  it("refresh and dispose do not throw", () => {
    stubFs({ knowledgePath: KB_PATH });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    expect(() => provider.refresh()).not.toThrow();
    expect(() => provider.dispose()).not.toThrow();
  });
});
