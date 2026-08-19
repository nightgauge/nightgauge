/**
 * getObservedComponents reads component:* labels off the service's own caches.
 *
 * The Component filter used to offer a hardcoded five-entry list
 * ("pattern-mining", "configs", "platform", "smart-setup", "standards") that
 * matched no issue in any workspace repository, so every option filtered to an
 * empty set. Options are now derived from real labels, and these tests pin the
 * two properties that matter: the values come from the cache, and building
 * them costs no network call.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { BoardItem } from "../../src/services/IpcClient";

const mockBoardList = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      githubRateLimit: vi.fn().mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value?: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  workspace: { getConfiguration: () => ({ get: () => undefined }) },
}));

function boardItem(number: number, labels: string[]): BoardItem {
  return {
    id: `item-${number}`,
    number,
    title: `Issue #${number}`,
    state: "OPEN",
    status: "Ready",
    priority: "",
    size: "",
    labels,
    assignees: [],
    repo: "nightgauge/nightgauge",
    url: `https://github.com/nightgauge/nightgauge/issues/${number}`,
    isEpic: false,
    blockedBy: [],
    blocking: [],
  } as BoardItem;
}

describe("ProjectBoardService.getObservedComponents", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    service = new ProjectBoardService("/test/workspace");
    (service as any).projectNumber = 1;
    (service as any).owner = "nightgauge";
    (service as any).configLoaded = true;
    vi.clearAllMocks();
  });

  it("returns nothing before anything is cached", () => {
    expect(service.getObservedComponents()).toEqual([]);
  });

  it("derives sorted, de-duplicated components from cached issues", async () => {
    mockBoardList.mockResolvedValue([
      boardItem(1, ["type:bug", "component:vscode"]),
      boardItem(2, ["component:go-binary"]),
      boardItem(3, ["component:vscode", "priority:high"]),
    ]);

    await service.getIssuesByStatus("ready");

    expect(service.getObservedComponents()).toEqual(["go-binary", "vscode"]);
  });

  it("costs no additional network call once the cache is warm", async () => {
    mockBoardList.mockResolvedValue([boardItem(1, ["component:sdk"])]);
    await service.getIssuesByStatus("ready");
    const callsAfterWarm = mockBoardList.mock.calls.length;

    service.getObservedComponents();
    service.getObservedComponents();

    expect(mockBoardList.mock.calls.length).toBe(callsAfterWarm);
  });

  it("returns nothing when cached issues carry no component labels", async () => {
    mockBoardList.mockResolvedValue([boardItem(1, ["type:bug", "priority:high"])]);

    await service.getIssuesByStatus("ready");

    // Callers use the empty result to omit the Component section entirely.
    expect(service.getObservedComponents()).toEqual([]);
  });

  it("unions components across separately cached statuses", async () => {
    mockBoardList.mockResolvedValueOnce([boardItem(1, ["component:ci"])]);
    await service.getIssuesByStatus("ready");
    mockBoardList.mockResolvedValueOnce([boardItem(2, ["component:docs"])]);
    await service.getIssuesByStatus("backlog");

    expect(service.getObservedComponents()).toEqual(["ci", "docs"]);
  });
});
