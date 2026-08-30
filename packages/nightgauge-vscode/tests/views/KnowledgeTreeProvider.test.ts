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
import type { ConfigBridge } from "../../src/services/ConfigBridge";

vi.mock("vscode", () => {
  const EventEmitter = vi.fn(function (this: Record<string, unknown>) {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  });
  return {
    EventEmitter,
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: vi.fn(function (
      this: Record<string, unknown>,
      label: string,
      collapsibleState: number
    ) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }),
    ThemeIcon: vi.fn(function (this: Record<string, unknown>, id: string, color?: any) {
      this.id = id;
      this.color = color;
    }),
    ThemeColor: vi.fn(function (this: Record<string, unknown>, id: string) {
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
    RelativePattern: vi.fn(function (this: Record<string, unknown>, base: string, pattern: string) {
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

/**
 * Stub fs so that `issue-42.json` exists at exactly ONE absolute path.
 *
 * The suffix-matching `stubFs` above answers the same for every layout, which
 * is precisely the blindness that let #1206 ship: the provider read the
 * workspace root only, and a stub keyed on `endsWith("issue-42.json")` is happy
 * either way. These cases assert the path, not the suffix.
 */
function stubFsAt(opts: {
  contextFileAt: string;
  knowledgePath: string;
  knowledgeRead?: string[];
}): void {
  vi.mocked(fsModule.readFileSync).mockImplementation((p: unknown) => {
    const filePath = p as string;
    if (filePath === opts.contextFileAt) {
      return JSON.stringify({ knowledge_path: opts.knowledgePath });
    }
    if (filePath.endsWith("planning-42.json")) {
      return JSON.stringify({ knowledge_read: opts.knowledgeRead ?? [] });
    }
    throw new Error(`ENOENT: ${filePath}`);
  });
  vi.mocked(fsModule.existsSync).mockImplementation(() => true);
}

function makeConfigBridge(values: Record<string, unknown>): ConfigBridge {
  return {
    getValue: vi.fn((k: string) => values[k]),
    onConfigChanged: vi.fn(() => ({ dispose: vi.fn() })),
  } as unknown as ConfigBridge;
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

  // --- worktree-resident context files (#1206) ---
  //
  // On the scheduler path issue-{N}.json is written INSIDE the run's worktree.
  // The provider read `<root>/.nightgauge/pipeline/issue-N.json` only, so it
  // found nothing on every real run and the section read "No knowledge base
  // scaffolded for this issue" for the life of the view. Go fixed the same
  // defect for its own readers in #994; this is the port.

  it("resolves a context file in the VSCode worktree layout, with nothing at the root", async () => {
    stubFsAt({
      contextFileAt: "/workspace/.worktrees/issue-42/.nightgauge/pipeline/issue-42.json",
      knowledgePath: KB_PATH,
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const children = await provider.getChildren(root[0]);
    expect(children.map((c) => c.label)).toEqual(["PRD.md", "decisions.md", "outcomes.md"]);
    provider.dispose();
  });

  it("resolves a context file in the Go manager worktree layout", async () => {
    // `.nightgauge/worktrees/{repoName}-issue-N` — the leaf carries the repo
    // name so two repos' issue #N cannot collide in one workspace.
    stubFsAt({
      contextFileAt:
        "/workspace/.nightgauge/worktrees/nightgauge-issue-42/.nightgauge/pipeline/issue-42.json",
      knowledgePath: KB_PATH,
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient(),
      makeConfigBridge({ repo: "nightgauge/nightgauge" })
    );
    const root = await provider.getChildren();
    const children = await provider.getChildren(root[0]);
    expect(children.map((c) => c.label)).toEqual(["PRD.md", "decisions.md", "outcomes.md"]);
    provider.dispose();
  });

  it("still resolves a context file at the plain workspace root", async () => {
    // A run that never took a worktree. Adding candidates must not lose the
    // one layout that already worked.
    stubFsAt({
      contextFileAt: "/workspace/.nightgauge/pipeline/issue-42.json",
      knowledgePath: KB_PATH,
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const children = await provider.getChildren(root[0]);
    expect(children.map((c) => c.label)).toEqual(["PRD.md", "decisions.md", "outcomes.md"]);
    provider.dispose();
  });

  it("highlights knowledge_read entries from a worktree-resident planning file", async () => {
    stubFsAt({
      contextFileAt: "/workspace/.worktrees/issue-42/.nightgauge/pipeline/issue-42.json",
      knowledgePath: KB_PATH,
      knowledgeRead: [`${KB_PATH}/PRD.md`],
    });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient()
    );
    const root = await provider.getChildren();
    const children = (await provider.getChildren(root[0])) as KnowledgeActiveFileItem[];
    const prd = children.find((c) => c.filePath?.endsWith("PRD.md"));
    const decisions = children.find((c) => c.filePath?.endsWith("decisions.md"));
    expect(prd?.highlighted).toBe(true);
    expect(decisions?.highlighted).toBe(false);
    provider.dispose();
  });

  // --- disabled vs not scaffolded (#1206) ---

  it("distinguishes a disabled knowledge base from an unscaffolded one", async () => {
    stubFs({ knowledgePath: null });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient(),
      makeConfigBridge({ "knowledge.enabled": false })
    );
    const root = await provider.getChildren();
    const children = await provider.getChildren(root[0]);
    expect(children[0].label).toBe("Knowledge base disabled (knowledge.enabled: false)");
    provider.dispose();
  });

  it("reports 'not scaffolded' when knowledge is enabled but the base is absent", async () => {
    stubFs({ knowledgePath: null });
    const provider = new KnowledgeTreeProvider(
      "/workspace",
      makePipelineState(42),
      makeIpcClient(),
      makeConfigBridge({ "knowledge.enabled": true })
    );
    const root = await provider.getChildren();
    const children = await provider.getChildren(root[0]);
    expect(children[0].label).toBe("No knowledge base scaffolded for this issue");
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
