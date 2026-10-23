/**
 * CompletedIssuesService.test.ts
 *
 * Unit tests for CompletedIssuesService — completed/failed issue tracking,
 * persistence, deduplication, FIFO eviction, retry count, and event emission.
 *
 * @see Issue #1502 - Pipeline failures show as Completed in sidebar
 * @see Issue #301  - Handle completed and failed issue states in pipeline
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode before any other imports
vi.mock("vscode", () => {
  class EventEmitter<T> {
    private handlers: Array<(e: T) => void> = [];
    event = (handler: (e: T) => void) => {
      this.handlers.push(handler);
      return { dispose: () => {} };
    };
    fire(e: T) {
      this.handlers.forEach((h) => h(e));
    }
    dispose() {
      this.handlers = [];
    }
  }

  return {
    EventEmitter,
    Disposable: class {
      dispose = vi.fn();
    },
  };
});

import { CompletedIssuesService } from "../../src/services/CompletedIssuesService";
import { MAX_COMPLETED_ISSUES } from "../../src/types/completedIssues";
import type { CompletedIssuesState } from "../../src/types/completedIssues";

// ── helpers ──────────────────────────────────────────────────────────────────

function makeMemento(initial: Record<string, unknown> = {}) {
  const store: Record<string, unknown> = { ...initial };
  return {
    get: vi.fn((key: string, defaultValue?: unknown) => (key in store ? store[key] : defaultValue)),
    update: vi.fn((key: string, value: unknown) => {
      store[key] = value;
      return Promise.resolve();
    }),
    _store: store,
    keys: vi.fn(() => Object.keys(store)),
  };
}

// ── setup ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  CompletedIssuesService.resetInstance();
});

afterEach(() => {
  CompletedIssuesService.resetInstance();
});

// ── getInstance ───────────────────────────────────────────────────────────────

describe("getInstance", () => {
  it("returns the same instance on repeated calls", () => {
    const memento = makeMemento();
    const a = CompletedIssuesService.getInstance(memento as any);
    const b = CompletedIssuesService.getInstance(memento as any);
    expect(a).toBe(b);
  });

  it("works without a memento (in-memory only)", () => {
    const service = CompletedIssuesService.getInstance();
    expect(service).toBeDefined();
    service.addCompleted(1, "Test", "feat/1-test");
    expect(service.getCompleted()).toHaveLength(1);
  });
});

// ── addCompleted ──────────────────────────────────────────────────────────────

describe("addCompleted", () => {
  it("adds an issue to the completed list", () => {
    const service = CompletedIssuesService.getInstance();
    service.addCompleted(42, "My feature", "feat/42-my-feature");
    const completed = service.getCompleted();
    expect(completed).toHaveLength(1);
    expect(completed[0].issue_number).toBe(42);
    expect(completed[0].title).toBe("My feature");
    expect(completed[0].branch).toBe("feat/42-my-feature");
  });

  it("deduplicates: second call with same issue number moves entry to front and updates timestamp", async () => {
    const service = CompletedIssuesService.getInstance();
    service.addCompleted(42, "My feature", "feat/42-my-feature");
    service.addCompleted(99, "Other", "feat/99-other");

    const tsFirst = service.getCompleted().find((c) => c.issue_number === 42)!.timestamp;

    // small delay so second timestamp is strictly different
    await new Promise((r) => setTimeout(r, 2));
    service.addCompleted(42, "My feature", "feat/42-my-feature");

    const completed = service.getCompleted();
    expect(completed).toHaveLength(2);
    expect(completed[0].issue_number).toBe(42); // moved to front
    expect(completed[0].timestamp).not.toBe(tsFirst); // timestamp updated
  });

  it("evicts oldest entries when MAX_COMPLETED_ISSUES is exceeded (FIFO)", () => {
    const service = CompletedIssuesService.getInstance();

    for (let i = 1; i <= MAX_COMPLETED_ISSUES + 5; i++) {
      service.addCompleted(i, `Issue ${i}`, `feat/${i}`);
    }

    const completed = service.getCompleted();
    expect(completed).toHaveLength(MAX_COMPLETED_ISSUES);
    // Most recently added (highest numbers) should be present
    const numbers = completed.map((c) => c.issue_number);
    expect(numbers).toContain(MAX_COMPLETED_ISSUES + 5);
    expect(numbers).not.toContain(1); // evicted
  });

  it("fires onStateChanged after mutation", () => {
    const service = CompletedIssuesService.getInstance();
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.addCompleted(1, "Test", "feat/1");
    expect(handler).toHaveBeenCalledOnce();
    const state: CompletedIssuesState = handler.mock.calls[0][0];
    expect(state.completed).toHaveLength(1);
  });

  it("persists state to the memento", () => {
    const memento = makeMemento();
    const service = CompletedIssuesService.getInstance(memento as any);
    service.addCompleted(7, "Persist me", "feat/7");
    expect(memento.update).toHaveBeenCalled();
  });
});

// ── addFailed ─────────────────────────────────────────────────────────────────

describe("addFailed", () => {
  it("adds an issue to the failed list", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(55, "Broken", "feat/55", "pr-create", "Something went wrong");
    const failed = service.getFailed();
    expect(failed).toHaveLength(1);
    expect(failed[0].issue_number).toBe(55);
    expect(failed[0].failed_stage).toBe("pr-create");
    expect(failed[0].retry_count).toBe(0);
  });

  it("increments retry_count on duplicate issue number", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(55, "Broken", "feat/55", "pr-create", "Error 1");
    service.addFailed(55, "Broken", "feat/55", "pr-create", "Error 2");
    const failed = service.getFailed();
    expect(failed).toHaveLength(1);
    expect(failed[0].retry_count).toBe(1);
    expect(failed[0].error).toContain("Error 2"); // most recent error
  });

  it("evicts oldest entries when MAX_COMPLETED_ISSUES is exceeded (FIFO)", () => {
    const service = CompletedIssuesService.getInstance();

    for (let i = 1; i <= MAX_COMPLETED_ISSUES + 3; i++) {
      service.addFailed(i, `Issue ${i}`, `feat/${i}`, "feature-dev", "err");
    }

    expect(service.getFailed()).toHaveLength(MAX_COMPLETED_ISSUES);
    const numbers = service.getFailed().map((f) => f.issue_number);
    expect(numbers).toContain(MAX_COMPLETED_ISSUES + 3);
    expect(numbers).not.toContain(1);
  });

  it("fires onStateChanged after mutation", () => {
    const service = CompletedIssuesService.getInstance();
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.addFailed(1, "Test", "feat/1", "feature-dev", "oops");
    expect(handler).toHaveBeenCalledOnce();
  });

  it("sanitizes home path from error message", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(1, "Test", "feat/1", "feature-dev", "/Users/john/secret/path");
    const failed = service.getFailed();
    expect(failed[0].error).toContain("~");
    expect(failed[0].error).not.toContain("/Users/john");
  });

  it("truncates errors longer than 500 characters", () => {
    const service = CompletedIssuesService.getInstance();
    const longError = "x".repeat(600);
    service.addFailed(1, "Test", "feat/1", "feature-dev", longError);
    const failed = service.getFailed();
    expect(failed[0].error.length).toBeLessThanOrEqual(500);
    expect(failed[0].error).toMatch(/\.\.\.$/);
  });
});

// ── getFailedIssue ────────────────────────────────────────────────────────────

describe("getFailedIssue", () => {
  it("returns the entry for a known issue number", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(42, "Broken", "feat/42", "feature-dev", "err");
    const entry = service.getFailedIssue(42);
    expect(entry).toBeDefined();
    expect(entry!.issue_number).toBe(42);
  });

  it("returns undefined when issue is not in the failed list", () => {
    const service = CompletedIssuesService.getInstance();
    expect(service.getFailedIssue(999)).toBeUndefined();
  });
});

// ── removeFromFailed ──────────────────────────────────────────────────────────

describe("removeFromFailed", () => {
  it("removes an issue from the failed list", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(10, "Test", "feat/10", "pr-create", "err");
    service.removeFromFailed(10);
    expect(service.getFailed()).toHaveLength(0);
  });

  it("fires onStateChanged when an entry is removed", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(10, "Test", "feat/10", "pr-create", "err");
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.removeFromFailed(10);
    expect(handler).toHaveBeenCalledOnce();
  });

  it("is a no-op (no event) when the issue is not in the list", () => {
    const service = CompletedIssuesService.getInstance();
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.removeFromFailed(999);
    expect(handler).not.toHaveBeenCalled();
  });
});

// ── clearCompleted / clearFailed ──────────────────────────────────────────────

describe("clearCompleted", () => {
  it("empties the completed array and fires onStateChanged", () => {
    const service = CompletedIssuesService.getInstance();
    service.addCompleted(1, "A", "feat/1");
    service.addCompleted(2, "B", "feat/2");
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.clearCompleted();
    expect(service.getCompleted()).toHaveLength(0);
    expect(handler).toHaveBeenCalledOnce();
  });
});

describe("clearFailed", () => {
  it("empties the failed array and fires onStateChanged", () => {
    const service = CompletedIssuesService.getInstance();
    service.addFailed(1, "A", "feat/1", "pr-create", "err");
    const handler = vi.fn();
    service.onStateChanged(handler);
    service.clearFailed();
    expect(service.getFailed()).toHaveLength(0);
    expect(handler).toHaveBeenCalledOnce();
  });
});

// ── loadState / persistence ───────────────────────────────────────────────────

describe("loadState", () => {
  it("restores persisted state from workspace storage on init", () => {
    const stored: CompletedIssuesState = {
      schema_version: "1.0",
      completed: [
        {
          issue_number: 5,
          title: "Persisted",
          branch: "feat/5",
          timestamp: new Date().toISOString(),
        },
      ],
      failed: [],
      updated_at: new Date().toISOString(),
    };
    const memento = makeMemento({ "nightgauge.devpletedIssues": stored });
    const service = CompletedIssuesService.getInstance(memento as any);
    expect(service.getCompleted()).toHaveLength(1);
    expect(service.getCompleted()[0].issue_number).toBe(5);
  });

  it("starts with empty state when storage is null", () => {
    const memento = makeMemento(); // nothing stored
    const service = CompletedIssuesService.getInstance(memento as any);
    expect(service.getCompleted()).toHaveLength(0);
    expect(service.getFailed()).toHaveLength(0);
  });

  it("ignores stored data with a wrong schema_version", () => {
    const corrupt = {
      schema_version: "99.0", // wrong version
      completed: [{ issue_number: 9, title: "Old", branch: "feat/9", timestamp: "" }],
      failed: [],
      updated_at: "",
    };
    const memento = makeMemento({ "nightgauge.devpletedIssues": corrupt });
    const service = CompletedIssuesService.getInstance(memento as any);
    // schema mismatch → falls back to empty state
    expect(service.getCompleted()).toHaveLength(0);
  });

  it("gracefully handles a storage read exception", () => {
    const memento = makeMemento();
    memento.get.mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    // Should not throw
    expect(() => CompletedIssuesService.getInstance(memento as any)).not.toThrow();
    const service = CompletedIssuesService.getInstance(memento as any);
    expect(service.getCompleted()).toHaveLength(0);
  });
});
