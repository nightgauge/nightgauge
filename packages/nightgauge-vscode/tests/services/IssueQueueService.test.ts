/**
 * IssueQueueService Tests — IPC Delegation Pattern
 *
 * Tests verify that the rewritten IssueQueueService delegates all operations
 * to the Go backend via IPC and relays queue.changed events.
 *
 * @see Issue #1898 - Consolidate Queue into Go
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { IssueQueueService } from "../../src/services/IssueQueueService";
import type { QueueCallbacks } from "../../src/types/queue";

// --- Mock IPC client ---

const mockQueueAdd = vi.fn().mockResolvedValue(undefined);
const mockQueueList = vi.fn().mockResolvedValue({
  schema_version: "2.0",
  status: "idle",
  items: [],
  updated_at: new Date().toISOString(),
});
const mockQueueRemove = vi.fn().mockResolvedValue(undefined);
const mockQueueClear = vi.fn().mockResolvedValue(undefined);
const mockQueueDequeueIndependent = vi.fn().mockResolvedValue([]);
const mockQueueEnqueueEpic = vi.fn().mockResolvedValue(undefined);

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
      queueAdd: mockQueueAdd,
      queueList: mockQueueList,
      queueRemove: mockQueueRemove,
      queueClear: mockQueueClear,
      queueDequeueIndependent: mockQueueDequeueIndependent,
      queueEnqueueEpic: mockQueueEnqueueEpic,
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

const mockGetRepoIdentity = vi.fn().mockResolvedValue({ owner: "test-owner", repo: "test-repo" });
vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: (...args: unknown[]) => mockGetRepoIdentity(...args),
}));

describe("IssueQueueService (IPC delegation)", () => {
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

  describe("enqueue()", () => {
    it("delegates to IPC queueAdd with correct params", async () => {
      const result = await service.enqueue(42, "Test issue", ["type:feature"]);

      // Trailing args are priority (unused here) and remoteRunId (#4120) — both
      // undefined for a plain enqueue with no repoOverride/runId.
      expect(mockQueueAdd).toHaveBeenCalledWith(
        "test-owner",
        "test-repo",
        42,
        "Test issue",
        ["type:feature"],
        undefined,
        undefined
      );
      expect(result).not.toBeNull();
      expect(result!.issueNumber).toBe(42);
      expect(result!.title).toBe("Test issue");
      expect(result!.status).toBe("pending");
    });

    it("forwards remoteRunId to IPC queueAdd (#4120)", async () => {
      await service.enqueue(42, "Test issue", ["type:feature"], undefined, {
        remoteRunId: "49b2019e-6ab7-4866-935e-235a32765bc7",
      });

      expect(mockQueueAdd).toHaveBeenCalledWith(
        "test-owner",
        "test-repo",
        42,
        "Test issue",
        ["type:feature"],
        undefined,
        "49b2019e-6ab7-4866-935e-235a32765bc7"
      );
    });

    it("returns null when getRepoIdentity fails", async () => {
      mockGetRepoIdentity.mockResolvedValueOnce(null);

      const result = await service.enqueue(42, "Test");

      expect(mockQueueAdd).not.toHaveBeenCalled();
      expect(result).toBeNull();
    });

    it("fires onItemAdded callback", async () => {
      const onItemAdded = vi.fn();
      service.setCallbacks({ onItemAdded });

      await service.enqueue(42, "Test issue");

      expect(onItemAdded).toHaveBeenCalledWith(
        expect.objectContaining({ issueNumber: 42, title: "Test issue" })
      );
    });

    it("routes epic issues to queueEnqueueEpic", async () => {
      await service.enqueue(100, "Epic issue", ["type:epic"]);

      expect(mockQueueEnqueueEpic).toHaveBeenCalledWith(
        "test-owner",
        "test-repo",
        100,
        "Epic issue",
        ["type:epic"]
      );
      expect(mockQueueAdd).not.toHaveBeenCalled();
    });

    it("refuses enqueue when shutdownGuard returns true", async () => {
      service.setShutdownGuard(() => true);

      const result = await service.enqueue(42, "Stop-race", ["type:feature"]);

      expect(result).toBeNull();
      expect(mockQueueAdd).not.toHaveBeenCalled();
      expect(mockQueueEnqueueEpic).not.toHaveBeenCalled();
    });

    it("refuses epic enqueue when shutdownGuard returns true", async () => {
      service.setShutdownGuard(() => true);

      const result = await service.enqueue(100, "Epic during stop", ["type:epic"]);

      expect(result).toBeNull();
      expect(mockQueueEnqueueEpic).not.toHaveBeenCalled();
    });

    it("refuses direct enqueueEpic() when shutdownGuard returns true", async () => {
      service.setShutdownGuard(() => true);

      const result = await service.enqueueEpic(100, "Epic direct", ["type:epic"]);

      expect(result).toBeNull();
      expect(mockQueueEnqueueEpic).not.toHaveBeenCalled();
    });

    it("still enqueues when shutdownGuard returns false", async () => {
      service.setShutdownGuard(() => false);

      const result = await service.enqueue(42, "Normal");

      expect(result).not.toBeNull();
      expect(mockQueueAdd).toHaveBeenCalled();
    });

    it("allows enqueue again after shutdownGuard is cleared (null)", async () => {
      service.setShutdownGuard(() => true);
      expect(await service.enqueue(42, "Blocked")).toBeNull();

      service.setShutdownGuard(null);

      const result = await service.enqueue(42, "Allowed");
      expect(result).not.toBeNull();
      expect(mockQueueAdd).toHaveBeenCalled();
    });

    it("shows blocked warning and cancels if user declines", async () => {
      const onBlockedWarning = vi.fn().mockResolvedValue(false);
      service.setCallbacks({ onBlockedWarning });

      const result = await service.enqueue(
        42,
        "Blocked issue",
        ["type:bug"],
        [
          {
            number: 99,
            title: "Blocker",
            url: "https://github.com/test/99",
            state: "OPEN" as const,
          },
        ]
      );

      expect(onBlockedWarning).toHaveBeenCalled();
      expect(result).toBeNull();
      expect(mockQueueAdd).not.toHaveBeenCalled();
    });
  });

  describe("dequeueIndependent()", () => {
    it("delegates to IPC and returns converted items", async () => {
      mockQueueDequeueIndependent.mockResolvedValueOnce([
        {
          repo: "test-owner/test-repo",
          issueNumber: 42,
          title: "Dequeued item",
          priority: 0,
          status: "pending",
          addedAt: "2026-01-01T00:00:00Z",
          position: 1,
        },
      ]);

      const items = await service.dequeueIndependent(2, [10, 20]);

      expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(2, [10, 20]);
      expect(items).toHaveLength(1);
      expect(items[0].issueNumber).toBe(42);
      expect(items[0].title).toBe("Dequeued item");
    });

    it("fires onItemRemoved callback for each dequeued item", async () => {
      const onItemRemoved = vi.fn();
      service.setCallbacks({ onItemRemoved });

      mockQueueDequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 1,
          title: "A",
          status: "pending",
          addedAt: "",
          position: 1,
          repo: "",
          priority: 0,
        },
        {
          issueNumber: 2,
          title: "B",
          status: "pending",
          addedAt: "",
          position: 2,
          repo: "",
          priority: 0,
        },
      ]);

      await service.dequeueIndependent(2, []);

      expect(onItemRemoved).toHaveBeenCalledTimes(2);
      expect(onItemRemoved).toHaveBeenCalledWith(1);
      expect(onItemRemoved).toHaveBeenCalledWith(2);
    });
  });

  describe("dequeue()", () => {
    it("delegates to dequeueIndependent(1, [])", async () => {
      mockQueueDequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 42,
          title: "Next",
          status: "pending",
          addedAt: "",
          position: 1,
          repo: "",
          priority: 0,
        },
      ]);

      const item = await service.dequeue();

      expect(mockQueueDequeueIndependent).toHaveBeenCalledWith(1, []);
      expect(item).not.toBeNull();
      expect(item!.issueNumber).toBe(42);
    });

    it("returns null when queue is empty", async () => {
      mockQueueDequeueIndependent.mockResolvedValueOnce([]);

      const item = await service.dequeue();

      expect(item).toBeNull();
    });
  });

  describe("remove()", () => {
    it("delegates to IPC queueRemove", async () => {
      const result = await service.remove(42);

      expect(mockQueueRemove).toHaveBeenCalledWith(42);
      expect(result).toBe(true);
    });

    it("fires onItemRemoved callback", async () => {
      const onItemRemoved = vi.fn();
      service.setCallbacks({ onItemRemoved });

      await service.remove(42);

      expect(onItemRemoved).toHaveBeenCalledWith(42);
    });
  });

  describe("clear()", () => {
    it("delegates to IPC queueClear", async () => {
      await service.clear();

      expect(mockQueueClear).toHaveBeenCalled();
    });

    it("fires onQueueCleared callback", async () => {
      const onQueueCleared = vi.fn();
      service.setCallbacks({ onQueueCleared });

      await service.clear();

      expect(onQueueCleared).toHaveBeenCalled();
    });
  });

  describe("getQueue()", () => {
    it("delegates to IPC queueList and converts state", async () => {
      mockQueueList.mockResolvedValueOnce({
        schema_version: "2.0",
        status: "waiting",
        items: [
          {
            repo: "test-owner/test-repo",
            issueNumber: 42,
            title: "Item",
            priority: 0,
            status: "pending",
            addedAt: "2026-01-01T00:00:00Z",
            position: 1,
          },
        ],
        updated_at: "2026-01-01T00:00:00Z",
      });

      const state = await service.getQueue();

      expect(state).not.toBeNull();
      expect(state!.schema_version).toBe("2.0");
      expect(state!.status).toBe("waiting");
      expect(state!.items).toHaveLength(1);
      expect(state!.items[0].issueNumber).toBe(42);
    });
  });

  describe("getQueueLength()", () => {
    it("returns item count from IPC", async () => {
      mockQueueList.mockResolvedValueOnce({
        schema_version: "2.0",
        status: "waiting",
        items: [
          {
            issueNumber: 1,
            title: "A",
            status: "pending",
            addedAt: "",
            position: 1,
            repo: "",
            priority: 0,
          },
          {
            issueNumber: 2,
            title: "B",
            status: "pending",
            addedAt: "",
            position: 2,
            repo: "",
            priority: 0,
          },
        ],
        updated_at: "",
      });

      const length = await service.getQueueLength();

      expect(length).toBe(2);
    });
  });

  describe("isQueued()", () => {
    it("returns true when issue is in queue", async () => {
      mockQueueList.mockResolvedValueOnce({
        schema_version: "2.0",
        status: "waiting",
        items: [
          {
            issueNumber: 42,
            title: "A",
            status: "pending",
            addedAt: "",
            position: 1,
            repo: "",
            priority: 0,
          },
        ],
        updated_at: "",
      });

      expect(await service.isQueued(42)).toBe(true);
    });

    it("returns false when issue is not in queue", async () => {
      mockQueueList.mockResolvedValueOnce({
        schema_version: "2.0",
        status: "idle",
        items: [],
        updated_at: "",
      });

      expect(await service.isQueued(42)).toBe(false);
    });
  });

  describe("queue.changed event relay", () => {
    it("fires onQueueChanged when IPC emits queue.changed", async () => {
      const listener = vi.fn();
      service.onQueueChanged(listener);

      // Simulate IPC event
      const handlers = eventHandlers.get("queue.changed");
      expect(handlers).toBeDefined();
      expect(handlers!.size).toBeGreaterThan(0);

      const ipcState = {
        schema_version: "2.0",
        status: "waiting",
        items: [
          {
            repo: "x/y",
            issueNumber: 42,
            title: "Changed",
            priority: 0,
            status: "pending",
            addedAt: "2026-01-01T00:00:00Z",
            position: 1,
          },
        ],
        updated_at: "2026-01-01T00:00:00Z",
      };

      for (const handler of handlers!) {
        handler(ipcState);
      }

      expect(listener).toHaveBeenCalledTimes(1);
      const firedState = listener.mock.calls[0][0];
      expect(firedState.schema_version).toBe("2.0");
      expect(firedState.items).toHaveLength(1);
      expect(firedState.items[0].issueNumber).toBe(42);
    });
  });

  describe("enqueueEpic()", () => {
    it("delegates to IPC queueEnqueueEpic", async () => {
      const result = await service.enqueueEpic(100, "Epic", ["type:epic"]);

      expect(mockQueueEnqueueEpic).toHaveBeenCalledWith("test-owner", "test-repo", 100, "Epic", [
        "type:epic",
      ]);
      expect(result).not.toBeNull();
      expect(result!.issueNumber).toBe(100);
    });
  });

  describe("getConfig() / setConfig()", () => {
    it("returns default config", () => {
      const config = service.getConfig();
      expect(config.maxQueueSize).toBe(20);
      expect(config.autoStart).toBe(true);
      expect(config.autoStartDelay).toBe(2000);
    });

    it("merges config updates", () => {
      service.setConfig({ autoStartDelay: 5000 });
      const config = service.getConfig();
      expect(config.autoStartDelay).toBe(5000);
      expect(config.autoStart).toBe(true);
    });
  });

  describe("singleton pattern", () => {
    it("returns same instance on multiple calls", () => {
      const instance1 = IssueQueueService.getInstance("/test/workspace");
      const instance2 = IssueQueueService.getInstance("/test/workspace");
      expect(instance1).toBe(instance2);
    });

    it("resets instance correctly", () => {
      const instance1 = IssueQueueService.getInstance("/test/workspace");
      IssueQueueService.resetInstance();
      const instance2 = IssueQueueService.getInstance("/test/workspace");
      expect(instance1).not.toBe(instance2);
    });
  });
});
