/**
 * Tests for SavedQueriesService — manages saved queries in the checkout's
 * saved-queries.yaml (`.git/nightgauge-worktree/`, ADR-024 § 7)
 *
 * Covers:
 * - getAll() — includes/excludes built-in queries based on constructor flag
 * - getUserQueries() / getBuiltInQueries()
 * - get(name) — finds by name, undefined for missing
 * - save() — add new, update existing, fires event, writes to file
 * - delete() — removes user query, returns false for built-in/missing
 * - rename() — renames, returns false when not found or new name taken
 * - recordUsage() — increments runCount, updates lastUsedAt
 * - File watcher — triggers reload on change
 * - dispose()
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "fs";
import * as yaml from "yaml";

// ---------------------------------------------------------------------------
// Vscode file watcher callback capture
// ---------------------------------------------------------------------------

const watcherCallbacks: {
  onChange?: (uri: unknown) => void;
  onCreate?: (uri: unknown) => void;
  onDelete?: (uri: unknown) => void;
} = {};

vi.mock("vscode", () => ({
  RelativePattern: class {
    constructor(
      public base: { fsPath: string },
      public pattern: string
    ) {}
  },
  Uri: { file: (p: string) => ({ fsPath: p, scheme: "file" }) },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn((cb: (uri: unknown) => void) => {
        watcherCallbacks.onChange = cb;
        return { dispose: vi.fn() };
      }),
      onDidCreate: vi.fn((cb: (uri: unknown) => void) => {
        watcherCallbacks.onCreate = cb;
        return { dispose: vi.fn() };
      }),
      onDidDelete: vi.fn((cb: (uri: unknown) => void) => {
        watcherCallbacks.onDelete = cb;
        return { dispose: vi.fn() };
      }),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn().mockReturnValue(false),
  readFileSync: vi.fn().mockReturnValue(""),
  writeFileSync: vi.fn(),
  mkdirSync: vi.fn(),
  promises: {
    mkdir: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("yaml", () => ({
  parse: vi.fn(),
  stringify: vi.fn().mockReturnValue('version: "1.0"\nqueries: []\n'),
}));

vi.mock("@nightgauge/sdk", async () => {
  const { z } = await import("zod");
  return {
    SavedQueriesFileSchema: z.object({
      version: z.string(),
      queries: z.array(
        z.object({
          name: z.string(),
          query: z.string(),
          description: z.string().optional(),
          createdAt: z.string().optional(),
        })
      ),
    }),
  };
});

vi.mock("../../src/types/QueryTypes", () => ({
  BUILTIN_QUERIES: [
    { name: "Ready Issues", query: "status:ready", isBuiltIn: true },
    { name: "In Progress", query: "status:in-progress", isBuiltIn: true },
  ],
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const ROOT = "/tmp/workspace";
const QUERIES_FILE = "/tmp/workspace/.git/nightgauge-worktree/saved-queries.yaml";

async function makeService(workspaceRoot = ROOT, includeBuiltIn = true) {
  // vi.resetModules() gives each test a fresh layout cache: fix the root's
  // layout in it (no git runs; fs is mocked, so nothing touches disk).
  const { fakeCloneLayout } = await import("../helpers/cloneLayout");
  fakeCloneLayout(ROOT);
  const { SavedQueriesService } = await import("../../src/services/SavedQueriesService");
  return new SavedQueriesService(workspaceRoot, includeBuiltIn);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("SavedQueriesService", () => {
  beforeEach(() => {
    vi.resetModules();
    watcherCallbacks.onChange = undefined;
    watcherCallbacks.onCreate = undefined;
    watcherCallbacks.onDelete = undefined;

    // Reset fs mocks
    vi.mocked(fs.existsSync).mockReturnValue(false);
    vi.mocked(fs.readFileSync).mockReturnValue("");
    vi.mocked(fs.writeFileSync).mockReset();
    vi.mocked(fs.mkdirSync).mockReset();

    // Reset yaml mocks
    vi.mocked(yaml.parse).mockReset();
    vi.mocked(yaml.stringify).mockReturnValue('version: "1.0"\nqueries: []\n');
  });

  // -------------------------------------------------------------------------
  // getAll()
  // -------------------------------------------------------------------------

  describe("getAll()", () => {
    it("includes built-in queries when includeBuiltIn=true", async () => {
      const svc = await makeService(ROOT, true);
      const all = svc.getAll();
      expect(all.some((q) => q.name === "Ready Issues")).toBe(true);
      expect(all.some((q) => q.name === "In Progress")).toBe(true);
    });

    it("excludes built-in queries when includeBuiltIn=false", async () => {
      const svc = await makeService(ROOT, false);
      const all = svc.getAll();
      expect(all.some((q) => q.isBuiltIn)).toBe(false);
    });
  });

  // -------------------------------------------------------------------------
  // getUserQueries() / getBuiltInQueries()
  // -------------------------------------------------------------------------

  describe("getUserQueries()", () => {
    it("returns empty when only built-ins are loaded", async () => {
      const svc = await makeService(ROOT, true);
      expect(svc.getUserQueries()).toHaveLength(0);
    });
  });

  describe("getBuiltInQueries()", () => {
    it("returns the built-in queries", async () => {
      const svc = await makeService(ROOT, true);
      const builtIns = svc.getBuiltInQueries();
      expect(builtIns).toHaveLength(2);
      expect(builtIns.every((q) => q.isBuiltIn)).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // get(name)
  // -------------------------------------------------------------------------

  describe("get(name)", () => {
    it("finds a query by name", async () => {
      const svc = await makeService(ROOT, true);
      const q = svc.get("Ready Issues");
      expect(q).toBeDefined();
      expect(q?.name).toBe("Ready Issues");
    });

    it("returns undefined for a missing query", async () => {
      const svc = await makeService(ROOT, true);
      expect(svc.get("Does Not Exist")).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // save()
  // -------------------------------------------------------------------------

  describe("save()", () => {
    it("adds a new user query", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Sprint Backlog", query: "status:ready" });
      expect(svc.get("Sprint Backlog")).toBeDefined();
    });

    it("updates an existing query", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Sprint Backlog", query: "status:ready" });
      await svc.save({
        name: "Sprint Backlog",
        query: "status:ready AND labels:sprint-42",
      });
      expect(svc.get("Sprint Backlog")?.query).toBe("status:ready AND labels:sprint-42");
      // Should not duplicate
      expect(svc.getUserQueries().filter((q) => q.name === "Sprint Backlog")).toHaveLength(1);
    });

    it("fires onQueriesChanged after save", async () => {
      const svc = await makeService(ROOT, true);
      const events: unknown[] = [];
      svc.onQueriesChanged((e: unknown) => events.push(e));

      await svc.save({ name: "New Query", query: "type:bug" });

      expect(events.length).toBeGreaterThanOrEqual(1);
    });

    it("writes to file after save", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Persist Me", query: "status:ready" });
      expect(vi.mocked(fs.writeFileSync)).toHaveBeenCalledWith(
        QUERIES_FILE,
        expect.any(String),
        "utf-8"
      );
    });

    it("refuses to save when the workspace root is not a git checkout", async () => {
      const svc = await makeService("relative/root", true);
      await expect(svc.save({ name: "Nowhere", query: "status:ready" })).rejects.toThrow(
        /not a git checkout/
      );
      expect(vi.mocked(fs.writeFileSync)).not.toHaveBeenCalled();
    });
  });

  // -------------------------------------------------------------------------
  // delete()
  // -------------------------------------------------------------------------

  describe("delete()", () => {
    it("removes a user query and returns true", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "To Delete", query: "status:ready" });
      expect(svc.get("To Delete")).toBeDefined();

      const result = await svc.delete("To Delete");
      expect(result).toBe(true);
      expect(svc.get("To Delete")).toBeUndefined();
    });

    it("returns false when query is not found", async () => {
      const svc = await makeService(ROOT, true);
      const result = await svc.delete("No Such Query");
      expect(result).toBe(false);
    });

    it("cannot delete built-in queries — returns false", async () => {
      const svc = await makeService(ROOT, true);
      const result = await svc.delete("Ready Issues");
      expect(result).toBe(false);
      expect(svc.get("Ready Issues")).toBeDefined();
    });
  });

  // -------------------------------------------------------------------------
  // rename()
  // -------------------------------------------------------------------------

  describe("rename()", () => {
    it("renames a user query and returns true", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Old Name", query: "type:bug" });

      const result = await svc.rename("Old Name", "New Name");
      expect(result).toBe(true);
      expect(svc.get("Old Name")).toBeUndefined();
      expect(svc.get("New Name")).toBeDefined();
    });

    it("returns false when original query not found", async () => {
      const svc = await makeService(ROOT, true);
      const result = await svc.rename("Nonexistent", "Something Else");
      expect(result).toBe(false);
    });

    it("returns false when new name already exists", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Query A", query: "type:bug" });
      await svc.save({ name: "Query B", query: "type:feature" });

      const result = await svc.rename("Query A", "Query B");
      expect(result).toBe(false);
      // Both should still exist unchanged
      expect(svc.get("Query A")).toBeDefined();
      expect(svc.get("Query B")).toBeDefined();
    });
  });

  // -------------------------------------------------------------------------
  // recordUsage()
  // -------------------------------------------------------------------------

  describe("recordUsage()", () => {
    it("increments runCount and updates lastUsedAt", async () => {
      const svc = await makeService(ROOT, true);
      await svc.save({ name: "Tracked Query", query: "status:ready" });

      const before = svc.get("Tracked Query");
      expect(before?.runCount).toBeUndefined();

      await svc.recordUsage("Tracked Query");

      const after = svc.get("Tracked Query");
      expect(after?.runCount).toBe(1);
      expect(after?.lastUsedAt).toBeDefined();

      await svc.recordUsage("Tracked Query");
      expect(svc.get("Tracked Query")?.runCount).toBe(2);
    });
  });

  // -------------------------------------------------------------------------
  // File watcher
  // -------------------------------------------------------------------------

  describe("file watcher", () => {
    it("watches saved-queries.yaml in the per-checkout directory", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.workspace.createFileSystemWatcher).mockClear();
      await makeService(ROOT, true);
      const pattern = vi.mocked(vscode.workspace.createFileSystemWatcher).mock
        .calls[0][0] as unknown as { base: { fsPath: string }; pattern: string };
      expect(pattern.base.fsPath).toBe("/tmp/workspace/.git/nightgauge-worktree");
      expect(pattern.pattern).toBe("saved-queries.yaml");
    });

    it("triggers reload when the file changes", async () => {
      const svc = await makeService(ROOT, true);

      // File now exists with no user queries initially
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue("version: '1.0'\nqueries: []\n");
      vi.mocked(yaml.parse).mockReturnValue({ version: "1.0", queries: [] });

      // Sanity: no user queries yet
      expect(svc.getUserQueries()).toHaveLength(0);

      // Simulate file now containing a new user query
      vi.mocked(yaml.parse).mockReturnValue({
        version: "1.0",
        queries: [
          {
            name: "From File",
            query: "status:ready",
          },
        ],
      });
      watcherCallbacks.onChange?.({});

      expect(svc.get("From File")).toBeDefined();
    });
  });

  // -------------------------------------------------------------------------
  // dispose()
  // -------------------------------------------------------------------------

  describe("dispose()", () => {
    it("disposes resources without throwing", async () => {
      const svc = await makeService(ROOT, true);
      expect(() => svc.dispose()).not.toThrow();
    });
  });
});
