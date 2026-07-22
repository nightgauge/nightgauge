/**
 * Tests for PipelineTreeProvider queue count display (Issue #306)
 *
 * Verifies that the tree view title shows the correct count of queued items
 * and updates dynamically when the queue changes.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import type { IssueQueueService } from "../../src/services/IssueQueueService";
import type { QueueState } from "../../src/types/queue";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

describe("PipelineTreeProvider - Count Display", () => {
  let provider: PipelineTreeProvider | null = null;
  let mockTreeView: vscode.TreeView<any>;
  let mockQueueService: IssueQueueService;

  beforeEach(() => {
    // Create mock TreeView with writable title property
    mockTreeView = {
      title: "",
      dispose: vi.fn(),
    } as any;

    // Create mock QueueService
    mockQueueService = {
      onQueueChanged: vi.fn((callback) => ({
        dispose: vi.fn(),
      })),
      getQueue: vi.fn(),
    } as any;

    vi.clearAllMocks();
  });

  afterEach(() => {
    if (provider) {
      provider.dispose();
      provider = null;
    }
  });

  describe("setTreeView()", () => {
    it("should set TreeView reference and update title", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Title should be updated with 0 count (no items in queue)
      expect(mockTreeView.title).toBe("Pipeline (0)");
    });
  });

  describe("updateViewTitle()", () => {
    it("should not update title if TreeView is not set", () => {
      provider = new PipelineTreeProvider();

      // This should not throw
      provider.updateViewTitle();

      // TreeView should not have been touched
      expect(mockTreeView.title).toBe("");
    });

    it("should update title with correct queue count format", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Simulate queue with 3 items by calling syncQueueFromState
      // (normally called via IssueQueueService.onQueueChanged)
      const mockQueueState: QueueState = {
        items: [
          { issueNumber: 1, title: "Issue 1", addedAt: Date.now() },
          { issueNumber: 2, title: "Issue 2", addedAt: Date.now() },
          { issueNumber: 3, title: "Issue 3", addedAt: Date.now() },
        ],
        status: "running",
      };

      // Access private method for testing (normally called internally)
      // @ts-ignore - accessing private method for testing
      provider.syncQueueFromState(mockQueueState);

      expect(mockTreeView.title).toBe("Pipeline (3)");
    });

    it("should show 0 when queue is empty", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Simulate empty queue
      // @ts-ignore - accessing private method for testing
      provider.syncQueueFromState(null);

      expect(mockTreeView.title).toBe("Pipeline (0)");
    });

    it("should show 0 when queue state has no items", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      const emptyQueueState: QueueState = {
        items: [],
        status: "idle",
      };

      // @ts-ignore - accessing private method for testing
      provider.syncQueueFromState(emptyQueueState);

      expect(mockTreeView.title).toBe("Pipeline (0)");
    });
  });

  describe("count updates when queue changes", () => {
    it("should update count when queue items are added", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Initial state: empty
      expect(mockTreeView.title).toBe("Pipeline (0)");

      // Add items to queue
      const queueState: QueueState = {
        items: [
          { issueNumber: 10, title: "Issue 10", addedAt: Date.now() },
          { issueNumber: 11, title: "Issue 11", addedAt: Date.now() },
        ],
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(queueState);

      expect(mockTreeView.title).toBe("Pipeline (2)");
    });

    it("should update count when queue items are removed", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Start with 3 items
      const initialState: QueueState = {
        items: [
          { issueNumber: 1, title: "Issue 1", addedAt: Date.now() },
          { issueNumber: 2, title: "Issue 2", addedAt: Date.now() },
          { issueNumber: 3, title: "Issue 3", addedAt: Date.now() },
        ],
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(initialState);
      expect(mockTreeView.title).toBe("Pipeline (3)");

      // Remove one item (simulating queue processing)
      const updatedState: QueueState = {
        items: [
          { issueNumber: 2, title: "Issue 2", addedAt: Date.now() },
          { issueNumber: 3, title: "Issue 3", addedAt: Date.now() },
        ],
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(updatedState);
      expect(mockTreeView.title).toBe("Pipeline (2)");
    });

    it("should update count when queue is cleared", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Start with items
      const initialState: QueueState = {
        items: [
          { issueNumber: 1, title: "Issue 1", addedAt: Date.now() },
          { issueNumber: 2, title: "Issue 2", addedAt: Date.now() },
        ],
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(initialState);
      expect(mockTreeView.title).toBe("Pipeline (2)");

      // Clear queue
      // @ts-ignore
      provider.syncQueueFromState(null);
      expect(mockTreeView.title).toBe("Pipeline (0)");
    });
  });

  describe("refreshAll()", () => {
    it("should update title when refreshAll() is called", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      const queueState: QueueState = {
        items: [{ issueNumber: 5, title: "Issue 5", addedAt: Date.now() }],
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(queueState);
      expect(mockTreeView.title).toBe("Pipeline (1)");

      // RefreshAll should update title again
      provider.refreshAll();
      expect(mockTreeView.title).toBe("Pipeline (1)");
    });
  });

  describe("edge cases", () => {
    it("should handle large queue counts correctly", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Create queue with 50 items
      const largeQueue: QueueState = {
        items: Array.from({ length: 50 }, (_, i) => ({
          issueNumber: i + 1,
          title: `Issue ${i + 1}`,
          addedAt: Date.now(),
        })),
        status: "running",
      };

      // @ts-ignore
      provider.syncQueueFromState(largeQueue);

      expect(mockTreeView.title).toBe("Pipeline (50)");
    });

    it("should not break if updateViewTitle() is called before queue setup", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Should show 0 for empty queue
      expect(mockTreeView.title).toBe("Pipeline (0)");
    });

    it("should handle rapid queue changes", () => {
      provider = new PipelineTreeProvider();
      provider.setTreeView(mockTreeView);

      // Simulate rapid queue updates
      for (let i = 1; i <= 10; i++) {
        const state: QueueState = {
          items: Array.from({ length: i }, (_, j) => ({
            issueNumber: j + 1,
            title: `Issue ${j + 1}`,
            addedAt: Date.now(),
          })),
          status: "running",
        };

        // @ts-ignore
        provider.syncQueueFromState(state);
        expect(mockTreeView.title).toBe(`Pipeline (${i})`);
      }
    });
  });
});
