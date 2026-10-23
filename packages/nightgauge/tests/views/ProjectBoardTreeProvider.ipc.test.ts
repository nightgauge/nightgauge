/**
 * ProjectBoardTreeProvider IPC Subscription Tests
 *
 * Verifies that each board tab subscribes to `tree.<tabId>.update` IPC events
 * and reactively refreshes on board mutations.
 *
 * @see Issue #1902 - Reactive UI Shell / Event-driven Tree Providers
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { TabId } from "../../src/types/TabConfig";

// --- Mock IPC client ---

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
      on: mockOn,
    }),
  },
}));

vi.mock("../../src/services/ProjectBoardService");

const TAB_IDS: TabId[] = ["in-progress", "in-review", "backlog"];

describe("ProjectBoardTreeProvider - IPC Subscription", () => {
  describe.each(TAB_IDS)("tab: %s", (tabId) => {
    let provider: ProjectBoardTreeProvider;
    let mockService: ProjectBoardService;

    beforeEach(() => {
      eventHandlers.clear();
      vi.clearAllMocks();

      // Ensure onDidChangeConfiguration returns a disposable
      vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
        dispose: vi.fn(),
      } as any);

      mockService = new ProjectBoardService("/test/workspace");
      provider = new ProjectBoardTreeProvider(mockService, tabId);
    });

    afterEach(() => {
      provider.dispose();
    });

    it(`subscribes to tree.${tabId}.update on construction`, () => {
      expect(mockOn).toHaveBeenCalledWith(`tree.${tabId}.update`, expect.any(Function));
    });

    it("fires onDidChangeTreeData when IPC event is received", () => {
      const eventName = `tree.${tabId}.update`;
      const handlers = eventHandlers.get(eventName);
      expect(handlers).toBeDefined();
      expect(handlers!.size).toBeGreaterThan(0);

      for (const handler of handlers!) {
        handler({ owner: "TestOrg", newStatus: tabId });
      }

      // The mock EventEmitter exposes fire as a vi.fn() spy
      const emitter = (provider as any)._onDidChangeTreeData;
      expect(emitter.fire).toHaveBeenCalled();
    });

    it("cleans up IPC subscription on dispose", () => {
      const eventName = `tree.${tabId}.update`;
      const handlers = eventHandlers.get(eventName);
      expect(handlers!.size).toBe(1);

      provider.dispose();

      expect(handlers!.size).toBe(0);
    });
  });

  it("different tabs subscribe to different events", () => {
    eventHandlers.clear();
    vi.clearAllMocks();

    // Ensure onDidChangeConfiguration returns a disposable
    vi.mocked(vscode.workspace.onDidChangeConfiguration).mockReturnValue({
      dispose: vi.fn(),
    } as any);

    const mockService = new ProjectBoardService("/test/workspace");
    const providers = TAB_IDS.map((tabId) => new ProjectBoardTreeProvider(mockService, tabId));

    // Each tab should have registered its own event
    for (const tabId of TAB_IDS) {
      const eventName = `tree.${tabId}.update`;
      expect(eventHandlers.has(eventName)).toBe(true);
      expect(eventHandlers.get(eventName)!.size).toBe(1);
    }

    // Clean up
    for (const p of providers) {
      p.dispose();
    }
  });
});
