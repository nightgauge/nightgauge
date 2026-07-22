import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as path from "node:path";
import { ActiveIssueKnowledgeProvider } from "../../src/providers/ActiveIssueKnowledgeProvider";
import {
  ActiveIssueKnowledgeSectionItem,
  ActiveIssueKnowledgeFileItem,
  ActiveIssueKnowledgeRecallItem,
  ActiveIssueKnowledgeEmptyItem,
} from "../../src/providers/items/ActiveIssueKnowledgeTreeItem";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { IpcClient } from "../../src/services/IpcClient";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  const EventEmitter = vi.fn(function () {
    return {
      event: vi.fn(),
      fire: vi.fn(),
      dispose: vi.fn(),
    };
  });
  return {
    EventEmitter,
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: vi.fn(function (label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }),
    ThemeIcon: vi.fn(function (id: string) {
      this.id = id;
    }),
    ThemeColor: vi.fn(function (id: string) {
      this.id = id;
    }),
    Uri: {
      file: (p: string) => ({ fsPath: p }),
    },
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
    commands: {
      executeCommand: vi.fn(),
    },
  };
});

vi.mock("node:fs", () => ({
  existsSync: vi.fn(() => true),
  readFileSync: vi.fn(),
}));

import * as fsModule from "node:fs";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makePipelineStateService(issueNumber: number | null): PipelineStateService {
  const mockDisposable = { dispose: vi.fn() };
  return {
    getActiveIssueBlockingPickup: vi.fn(() => issueNumber),
    onStateChanged: vi.fn(() => mockDisposable),
  } as unknown as PipelineStateService;
}

function makeIpcClient(
  relatedHits: { path: string; snippet: string; score: number; issue_number?: number }[] = [],
  shouldThrow = false
): IpcClient {
  return {
    knowledgeRelatedToIssue: vi.fn(async () => {
      if (shouldThrow) throw new Error("ipc failed");
      return {
        hits: relatedHits.map((h, i) => ({
          rank: i + 1,
          score: h.score,
          path: h.path,
          kind: "issue",
          issue_number: h.issue_number,
          snippet: h.snippet,
        })),
      };
    }),
  } as unknown as IpcClient;
}

function makeProvider(
  issueNumber: number | null,
  knowledgePath: string | null = null,
  ipcClient: IpcClient = makeIpcClient()
): ActiveIssueKnowledgeProvider {
  const pss = makePipelineStateService(issueNumber);
  vi.mocked(fsModule.readFileSync).mockImplementation((p: unknown) => {
    const filePath = p as string;
    if (filePath.includes("issue-") && filePath.endsWith(".json")) {
      return JSON.stringify({ knowledge_path: knowledgePath });
    }
    throw new Error(`ENOENT: ${filePath}`);
  });
  return new ActiveIssueKnowledgeProvider("/workspace", pss, ipcClient);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ActiveIssueKnowledgeProvider (IPC-migrated #2964)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fsModule.existsSync).mockReturnValue(true);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("Scenario 1: No active issue", () => {
    it("returns a single empty-state item when no issue is active", async () => {
      const provider = makeProvider(null);
      const children = await provider.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0]).toBeInstanceOf(ActiveIssueKnowledgeEmptyItem);
      expect((children[0] as ActiveIssueKnowledgeEmptyItem).label).toContain("No active issue");
    });
  });

  describe("Scenario 2: Active issue with full KB", () => {
    it("returns 3 section items with correct file leaves", async () => {
      const kbPath = "/workspace/.nightgauge/knowledge/features/42-my-feature";
      vi.mocked(fsModule.existsSync).mockReturnValue(true);
      const provider = makeProvider(42, kbPath);
      const children = await provider.getChildren();
      expect(children).toHaveLength(3);
      expect((children[0] as ActiveIssueKnowledgeSectionItem).sectionKind).toBe("prd");
      expect((children[1] as ActiveIssueKnowledgeSectionItem).sectionKind).toBe("decisions");
      expect((children[2] as ActiveIssueKnowledgeSectionItem).sectionKind).toBe("recall");

      const prdChildren = await provider.getChildren(
        children[0] as ActiveIssueKnowledgeSectionItem
      );
      expect(prdChildren).toHaveLength(1);
      expect(prdChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeFileItem);
      expect((prdChildren[0] as ActiveIssueKnowledgeFileItem).filePath).toBe(
        path.join(kbPath, "PRD.md")
      );
    });
  });

  describe("Scenario 3: Missing decisions.md", () => {
    it("shows 'No decisions yet' leaf when decisions.md does not exist", async () => {
      const kbPath = "/workspace/.nightgauge/knowledge/features/42-my-feature";
      vi.mocked(fsModule.existsSync).mockImplementation((p: unknown) => {
        const filePath = p as string;
        return !filePath.endsWith("decisions.md");
      });
      const provider = makeProvider(42, kbPath);
      const children = await provider.getChildren();
      const decChildren = await provider.getChildren(
        children[1] as ActiveIssueKnowledgeSectionItem
      );
      expect(decChildren).toHaveLength(1);
      expect(decChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeEmptyItem);
    });
  });

  describe("Scenario 4: Recall returns N results via IPC", () => {
    it("returns N recall items under Related Decisions, calling IPC", async () => {
      const kbPath = "/workspace/.nightgauge/knowledge/features/42-my-feature";
      vi.mocked(fsModule.existsSync).mockReturnValue(true);
      const hits = [
        { path: "/x/decisions.md", snippet: "Use singleton pattern", score: 2.5, issue_number: 10 },
        { path: "/y/decisions.md", snippet: "Inject deps", score: 2.0, issue_number: 11 },
      ];
      const ipc = makeIpcClient(hits);
      const provider = makeProvider(42, kbPath, ipc);
      const children = await provider.getChildren();
      const recallChildren = await provider.getChildren(
        children[2] as ActiveIssueKnowledgeSectionItem
      );
      expect(ipc.knowledgeRelatedToIssue).toHaveBeenCalledWith(42, 10);
      expect(recallChildren).toHaveLength(2);
      expect(recallChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeRecallItem);
    });
  });

  describe("Recall error handling", () => {
    it("returns an informational item when IPC throws", async () => {
      const kbPath = "/workspace/.nightgauge/knowledge/features/42-my-feature";
      vi.mocked(fsModule.existsSync).mockReturnValue(true);
      const ipc = makeIpcClient([], true);
      const provider = makeProvider(42, kbPath, ipc);
      const children = await provider.getChildren();
      const recallChildren = await provider.getChildren(
        children[2] as ActiveIssueKnowledgeSectionItem
      );
      expect(recallChildren).toHaveLength(1);
      expect(recallChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeEmptyItem);
    });
  });
});
