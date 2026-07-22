/**
 * IssueQueueService.dequeueIndependent — IPC delegation tests
 *
 * The dequeueIndependent algorithm now lives in Go. These tests verify that
 * the TypeScript wrapper correctly delegates to IPC and converts results.
 *
 * @see Issue #1898 - Consolidate Queue into Go
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { IssueQueueService } from "../../src/services/IssueQueueService";

// --- Mock IPC client ---

const mockQueueDequeueIndependent = vi.fn().mockResolvedValue([]);
const mockQueueList = vi.fn().mockResolvedValue({
  schema_version: "2.0",
  status: "idle",
  items: [],
  updated_at: new Date().toISOString(),
});

const eventHandlers = new Map<string, Set<(data: unknown) => void>>();
const mockOn = vi.fn((event: string, handler: (data: unknown) => void) => {
  if (!eventHandlers.has(event)) {
    eventHandlers.set(event, new Set());
  }
  eventHandlers.get(event)!.add(handler);
  return { dispose: () => eventHandlers.get(event)?.delete(handler) };
});

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      queueAdd: vi.fn().mockResolvedValue(undefined),
      queueList: mockQueueList,
      queueRemove: vi.fn().mockResolvedValue(undefined),
      queueClear: vi.fn().mockResolvedValue(undefined),
      queueDequeueIndependent: mockQueueDequeueIndependent,
      queueEnqueueEpic: vi.fn().mockResolvedValue(undefined),
      on: mockOn,
    }),
  },
}));

// --- Mock vscode ---

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(data: any) => void> = [];
    fire(data: any) {
      this.listeners.forEach((l) => l(data));
    }
    event = (listener: (data: any) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
}));

// --- Mock getRepoIdentity ---

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn().mockResolvedValue({ owner: "test-owner", repo: "test-repo" }),
}));

function makeIpcItem(
  overrides: Partial<{
    repo: string;
    issueNumber: number;
    title: string;
    priority: number;
    status: string;
    addedAt: string;
    position: number;
    labels: string[];
    blockedBy: Array<{ number: number; title: string; state: string }>;
    epicOrder: number;
  }>
) {
  return {
    repo: "test-owner/test-repo",
    issueNumber: 1,
    title: "Item",
    priority: 0,
    status: "pending",
    addedAt: "2026-01-01T00:00:00Z",
    position: 1,
    ...overrides,
  };
}

describe("IssueQueueService.dequeueIndependent", () => {
  let service: IssueQueueService;

  beforeEach(() => {
    IssueQueueService.resetInstance();
    eventHandlers.clear();
    vi.clearAllMocks();
    service = IssueQueueService.getInstance("/test/workspace");
  });

  afterEach(() => {
    IssueQueueService.resetInstance();
  });

  it("returns empty array for empty queue", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([]);

    const result = await service.dequeueIndependent(2, []);

    expect(result).toEqual([]);
    expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(2, []);
  });

  it("dequeues up to maxSlots independent items", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([
      makeIpcItem({ issueNumber: 1, title: "Issue 1", position: 1 }),
      makeIpcItem({ issueNumber: 2, title: "Issue 2", position: 2 }),
    ]);

    const result = await service.dequeueIndependent(2, []);

    expect(result).toHaveLength(2);
    expect(result[0].issueNumber).toBe(1);
    expect(result[1].issueNumber).toBe(2);
    expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(2, []);
  });

  it("passes runningItems (repo + number) to IPC for blocking + per-repo cap check", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([
      makeIpcItem({ issueNumber: 1, title: "Independent" }),
      makeIpcItem({ issueNumber: 3, title: "Also independent" }),
    ]);

    // Real signature is Array<{repo, number}> — the repo is what lets Go
    // enforce the per-repo concurrency cap (concurrency.per_repo_max). A bare
    // number would drop the repo and silently disable the cap (#3874).
    const running = [{ repo: "o/A", number: 100 }];
    const result = await service.dequeueIndependent(2, running);

    expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(2, running);
    expect(result).toHaveLength(2);
  });

  it("passes multiple running items (each carrying its repo) to IPC", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([]);

    const running = [
      { repo: "o/A", number: 99 },
      { repo: "o/A", number: 100 },
      { repo: "o/B", number: 101 },
    ];
    await service.dequeueIndependent(5, running);

    expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(5, running);
  });

  it("returns fewer than maxSlots when Go returns fewer", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([]);

    const result = await service.dequeueIndependent(5, [{ repo: "o/A", number: 99 }]);

    expect(result).toEqual([]);
  });

  it("fires onItemRemoved callback for each dequeued item", async () => {
    const onItemRemoved = vi.fn();
    service.setCallbacks({ onItemRemoved });

    mockQueueDequeueIndependent.mockResolvedValueOnce([
      makeIpcItem({ issueNumber: 10, title: "First" }),
      makeIpcItem({ issueNumber: 30, title: "Independent" }),
    ]);

    await service.dequeueIndependent(3, []);

    expect(onItemRemoved).toHaveBeenCalledTimes(2);
    expect(onItemRemoved).toHaveBeenCalledWith(10);
    expect(onItemRemoved).toHaveBeenCalledWith(30);
  });

  it("converts IPC items to QueueItem format with blockedBy", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([
      makeIpcItem({
        issueNumber: 10,
        title: "Sub 1",
        epicOrder: 0,
        blockedBy: [{ number: 5, title: "Blocker", state: "CLOSED" }],
      }),
    ]);

    const result = await service.dequeueIndependent(1, []);

    expect(result).toHaveLength(1);
    expect(result[0].issueNumber).toBe(10);
    expect(result[0].title).toBe("Sub 1");
    expect(result[0].epicOrder).toBe(0);
    expect(result[0].blockedBy).toHaveLength(1);
    expect(result[0].blockedBy![0].number).toBe(5);
    expect(result[0].blockedBy![0].state).toBe("CLOSED");
    expect(result[0].blockedBy![0].url).toBe("");
  });

  it("handles IPC items without optional fields", async () => {
    mockQueueDequeueIndependent.mockResolvedValueOnce([
      {
        repo: "x/y",
        issueNumber: 42,
        title: "Simple",
        priority: 0,
        status: "pending",
        addedAt: "2026-01-01T00:00:00Z",
        position: 1,
      },
    ]);

    const result = await service.dequeueIndependent(1, []);

    expect(result).toHaveLength(1);
    expect(result[0].issueNumber).toBe(42);
    expect(result[0].blockedBy).toBeUndefined();
    expect(result[0].epicOrder).toBeUndefined();
  });
});
