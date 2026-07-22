/**
 * Tests for QueryResultsTreeProvider — TreeDataProvider for query results
 *
 * Covers:
 * - Idle state rendering
 * - Parsing/executing spinner state
 * - Error state with message and retry action
 * - Complete state with summary and issue items
 * - Empty results display
 * - Issue item properties (description, icon, tooltip, command)
 * - Refresh fires onDidChangeTreeData
 * - dispose() cleans up resources
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  class MockEventEmitter {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value?: unknown) {
      for (const h of this._handlers) h(value as unknown);
    }
    dispose() {}
  }

  return {
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: class {
      label: string;
      collapsibleState: number;
      description?: string;
      tooltip?: unknown;
      iconPath?: unknown;
      command?: unknown;
      contextValue?: string;
      constructor(label: string, collapsibleState = 0) {
        this.label = label;
        this.collapsibleState = collapsibleState;
      }
    },
    ThemeIcon: class {
      id: string;
      color?: unknown;
      constructor(id: string, color?: unknown) {
        this.id = id;
        this.color = color;
      }
    },
    ThemeColor: class {
      id: string;
      constructor(id: string) {
        this.id = id;
      }
    },
    MarkdownString: class {
      value = "";
      appendMarkdown(s: string) {
        this.value += s;
      }
    },
    Uri: {
      parse: (url: string) => ({ toString: () => url, scheme: "https", path: url }),
    },
    EventEmitter: MockEventEmitter,
  };
});

// ---------------------------------------------------------------------------
// Mock BaseTreeItem — must come before importing the provider
// ---------------------------------------------------------------------------

vi.mock("../../src/views/items/BaseTreeItem", () => {
  class BaseTreeItem {
    label: string;
    collapsibleState: number;
    description?: string;
    tooltip?: unknown;
    iconPath?: unknown;
    command?: unknown;
    contextValue?: string;
    protected children: unknown[] = [];

    constructor(label: string, collapsibleState = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
    getChildren() {
      return this.children;
    }
    addChild(child: unknown) {
      this.children.push(child);
    }
    clearChildren() {
      this.children = [];
    }
    protected setIcon(codicon: string) {
      this.iconPath = { id: codicon };
    }
    protected setIconWithColor(codicon: string, color: unknown) {
      this.iconPath = { id: codicon, color };
    }
  }

  return { BaseTreeItem };
});

vi.mock("../../src/views/items/ReadyIssueTreeItem", () => ({
  ReadyIssueTreeItem: class {},
}));

// ---------------------------------------------------------------------------
// Import after mocks
// ---------------------------------------------------------------------------

import { QueryResultsTreeProvider } from "../../src/views/QueryResultsTreeProvider";
import type { QueryContext } from "../../src/types/QueryTypes";
import type { QueryResult, QueryableIssue } from "@nightgauge/sdk";

// ---------------------------------------------------------------------------
// QueryService stub
// ---------------------------------------------------------------------------

function createMockQueryService() {
  type Handler = (ctx: QueryContext) => void;
  const handlers: Handler[] = [];

  return {
    onQueryStateChanged: (cb: Handler) => {
      handlers.push(cb);
      return { dispose: vi.fn() };
    },
    _fire(ctx: QueryContext) {
      for (const h of handlers) h(ctx);
    },
  };
}

// ---------------------------------------------------------------------------
// Test issue factory
// ---------------------------------------------------------------------------

function makeIssue(overrides: Partial<QueryableIssue> = {}): QueryableIssue {
  return {
    number: 1,
    title: "Test issue",
    labels: [],
    priority: null,
    size: null,
    url: "https://github.com/test/repo/issues/1",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("QueryResultsTreeProvider", () => {
  let mockService: ReturnType<typeof createMockQueryService>;
  let provider: QueryResultsTreeProvider;

  beforeEach(() => {
    mockService = createMockQueryService();
    provider = new QueryResultsTreeProvider(mockService as any);
  });

  // -----------------------------------------------------------------------
  // Idle state
  // -----------------------------------------------------------------------

  describe("idle state", () => {
    it("shows 'Run a query' prompt when idle", async () => {
      const children = await provider.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("Run a query to see results");
    });

    it("idle item has a command to open query input", async () => {
      const children = await provider.getChildren();
      expect(children[0].command).toEqual({
        command: "nightgauge.queryProjectItems",
        title: "Run Query",
      });
    });
  });

  // -----------------------------------------------------------------------
  // Parsing / executing state
  // -----------------------------------------------------------------------

  describe("parsing/executing state", () => {
    it("shows spinner message when parsing", async () => {
      mockService._fire({ query: "status:ready", state: "parsing" });
      const children = await provider.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("Executing query...");
    });

    it("shows spinner message when executing", async () => {
      mockService._fire({ query: "status:ready", state: "executing" });
      const children = await provider.getChildren();
      expect(children).toHaveLength(1);
      expect(children[0].label).toBe("Executing query...");
    });
  });

  // -----------------------------------------------------------------------
  // Error state
  // -----------------------------------------------------------------------

  describe("error state", () => {
    it("shows error message and retry action", async () => {
      mockService._fire({
        query: "badfield:value",
        state: "error",
        error: "Unknown field: badfield",
      });
      const children = await provider.getChildren();
      expect(children).toHaveLength(2);
      expect(children[0].label).toBe("Error: Unknown field: badfield");
      expect(children[1].label).toBe("Try again");
    });

    it("shows generic message when error is undefined", async () => {
      mockService._fire({ query: "bad", state: "error" });
      const children = await provider.getChildren();
      expect(children[0].label).toBe("Error: Unknown error");
    });

    it("retry action has command to open query input", async () => {
      mockService._fire({ query: "bad", state: "error", error: "fail" });
      const children = await provider.getChildren();
      expect(children[1].command).toEqual({
        command: "nightgauge.queryProjectItems",
        title: "Run Query",
      });
    });
  });

  // -----------------------------------------------------------------------
  // Complete state — with results
  // -----------------------------------------------------------------------

  describe("complete state with results", () => {
    const result: QueryResult = {
      items: [
        makeIssue({ number: 101, title: "Auth bug", priority: "P0", size: "M", status: "ready" }),
        makeIssue({
          number: 102,
          title: "Dark mode",
          priority: "P1",
          size: "L",
          status: "in-progress",
        }),
      ],
      matchCount: 2,
      totalCount: 10,
      executionTimeMs: 5,
    };

    beforeEach(() => {
      mockService._fire({ query: "status:ready", state: "complete", result });
    });

    it("shows summary item with counts", async () => {
      const children = await provider.getChildren();
      expect(children[0].label).toBe("2 of 10 issues");
      expect(children[0].description).toBe("Query: status:ready");
    });

    it("shows issue items after summary", async () => {
      const children = await provider.getChildren();
      // 1 summary + 2 issues
      expect(children).toHaveLength(3);
      expect(children[1].label).toBe("#101 Auth bug");
      expect(children[2].label).toBe("#102 Dark mode");
    });

    it("issue item has priority/size/status in description", async () => {
      const children = await provider.getChildren();
      // #101: P0 · M · ready
      expect(children[1].description).toContain("P0");
      expect(children[1].description).toContain("M");
      expect(children[1].description).toContain("ready");
    });

    it("P0 issue gets flame icon", async () => {
      const children = await provider.getChildren();
      expect((children[1].iconPath as any)?.id).toBe("flame");
    });

    it("P1 issue gets arrow-up icon", async () => {
      const children = await provider.getChildren();
      expect((children[2].iconPath as any)?.id).toBe("arrow-up");
    });

    it("issue item has contextValue for menu contributions", async () => {
      const children = await provider.getChildren();
      expect(children[1].contextValue).toBe("queryResultIssue");
    });

    it("issue item has command to open on GitHub", async () => {
      const children = await provider.getChildren();
      const cmd = children[1].command as any;
      expect(cmd?.command).toBe("vscode.open");
    });
  });

  // -----------------------------------------------------------------------
  // Complete state — empty results
  // -----------------------------------------------------------------------

  describe("complete state with no results", () => {
    it("shows 'No matching issues' message", async () => {
      const result: QueryResult = {
        items: [],
        matchCount: 0,
        totalCount: 10,
        executionTimeMs: 2,
      };
      mockService._fire({ query: "status:cancelled", state: "complete", result });
      const children = await provider.getChildren();
      // Summary + "No matching issues"
      expect(children).toHaveLength(2);
      expect(children[1].label).toBe("No matching issues");
    });
  });

  // -----------------------------------------------------------------------
  // Complete state — null result guard
  // -----------------------------------------------------------------------

  describe("complete state with null result", () => {
    it("returns empty array when result is null", async () => {
      mockService._fire({ query: "status:ready", state: "complete", result: undefined });
      const children = await provider.getChildren();
      expect(children).toEqual([]);
    });
  });

  // -----------------------------------------------------------------------
  // Issue with no priority
  // -----------------------------------------------------------------------

  describe("issue without priority", () => {
    it("gets circle-outline icon for non-P0/P1", async () => {
      const result: QueryResult = {
        items: [makeIssue({ number: 1, title: "Low pri", priority: "P3" })],
        matchCount: 1,
        totalCount: 1,
        executionTimeMs: 1,
      };
      mockService._fire({ query: "priority:P3", state: "complete", result });
      const children = await provider.getChildren();
      expect((children[1].iconPath as any)?.id).toBe("circle-outline");
    });

    it("omits null fields from description", async () => {
      const result: QueryResult = {
        items: [makeIssue({ number: 1, title: "Bare issue" })],
        matchCount: 1,
        totalCount: 1,
        executionTimeMs: 1,
      };
      mockService._fire({ query: "number:1", state: "complete", result });
      const children = await provider.getChildren();
      // No priority, size, or status → empty description
      expect(children[1].description).toBe("");
    });
  });

  // -----------------------------------------------------------------------
  // TreeDataProvider contract
  // -----------------------------------------------------------------------

  describe("TreeDataProvider contract", () => {
    it("getTreeItem returns the element itself", () => {
      const item = { label: "test" } as any;
      expect(provider.getTreeItem(item)).toBe(item);
    });

    it("getChildren returns empty for non-root elements", async () => {
      const parent = { label: "parent" } as any;
      const children = await provider.getChildren(parent);
      expect(children).toEqual([]);
    });

    it("getParent returns null", () => {
      const item = { label: "test" } as any;
      expect(provider.getParent(item)).toBeNull();
    });
  });

  // -----------------------------------------------------------------------
  // Refresh / onDidChangeTreeData
  // -----------------------------------------------------------------------

  describe("refresh", () => {
    it("fires onDidChangeTreeData on refresh()", () => {
      const handler = vi.fn();
      provider.onDidChangeTreeData(handler);
      provider.refresh();
      expect(handler).toHaveBeenCalled();
    });

    it("fires onDidChangeTreeData when query state changes", () => {
      const handler = vi.fn();
      provider.onDidChangeTreeData(handler);
      mockService._fire({ query: "status:ready", state: "executing" });
      expect(handler).toHaveBeenCalled();
    });
  });

  // -----------------------------------------------------------------------
  // dispose()
  // -----------------------------------------------------------------------

  describe("dispose()", () => {
    it("disposes without throwing", () => {
      expect(() => provider.dispose()).not.toThrow();
    });
  });
});
