/**
 * Tests for the prefetch diagnostic getters added to ProjectBoardService.
 *
 * The Overview dashboard's "Project Board Summary" widget used to render
 * 0/0/0/0 for three visually identical scenarios:
 *   1. IPC failure (boardList threw)
 *   2. Board genuinely empty
 *   3. Board has items, but all from other repos (repo filter drops them)
 *
 * The dashboard now distinguishes these states by reading
 * `getLastPrefetchError()` and `getLastPrefetchDiagnostics()` after each
 * `prefetchAllItems()` call. This file pins those getters to the contract
 * the dashboard relies on.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";

// Match the existing service test mocks. Keep these in sync with
// ProjectBoardService.interface.test.ts.
const mockBoardList = vi.fn();
const mockConfigGetProjectConfig = vi.fn();
const mockBoardCounts = vi.fn();
const mockGithubRateLimit = vi.fn().mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      boardCounts: mockBoardCounts,
      configGetProjectConfig: mockConfigGetProjectConfig,
      githubRateLimit: mockGithubRateLimit,
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
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
  },
  Disposable: class {
    dispose() {}
  },
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getGitHubUser: vi.fn().mockReturnValue("test-user"),
}));

function makeBoardItem(overrides: Partial<Record<string, unknown>>) {
  return {
    number: 1,
    title: "Issue",
    status: "Ready",
    priority: "P2",
    repo: "test-org/test-repo",
    url: "https://example.com/1",
    labels: [],
    ...overrides,
  };
}

describe("ProjectBoardService — prefetch diagnostics", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    vi.clearAllMocks();
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "test-org",
      defaultRepo: "test-repo",
      projectNumber: 42,
      ownerType: "organization",
    });
    service = new ProjectBoardService("/test/workspace");
  });

  it("records diagnostics with raw + filtered counts when the prefetch succeeds", async () => {
    mockBoardList.mockResolvedValue([
      makeBoardItem({ number: 1, repo: "test-org/test-repo" }),
      makeBoardItem({ number: 2, repo: "test-org/test-repo" }),
      makeBoardItem({ number: 3, repo: "test-org/test-repo" }),
    ]);

    await service.prefetchAllItems({ force: true });

    expect(service.getLastPrefetchError()).toBeNull();
    expect(service.getLastPrefetchDiagnostics()).toEqual({
      rawItemCount: 3,
      filteredItemCount: 3,
      expectedRepo: "test-org/test-repo",
    });
  });

  it("reports rawItemCount > filteredItemCount when the repo filter drops items", async () => {
    // 5 items returned, only 1 matches the workspace's repo. This is the
    // exact "all from other repos" 0/0/0/0 scenario the dashboard needs to
    // distinguish from a truly-empty board.
    mockBoardList.mockResolvedValue([
      makeBoardItem({ number: 1, repo: "other-org/other-repo" }),
      makeBoardItem({ number: 2, repo: "other-org/other-repo" }),
      makeBoardItem({ number: 3, repo: "test-org/test-repo" }),
      makeBoardItem({ number: 4, repo: "yet-another/repo" }),
      makeBoardItem({ number: 5, repo: "yet-another/repo" }),
    ]);

    await service.prefetchAllItems({ force: true });

    const diag = service.getLastPrefetchDiagnostics()!;
    expect(diag.rawItemCount).toBe(5);
    expect(diag.filteredItemCount).toBe(1);
    expect(diag.expectedRepo).toBe("test-org/test-repo");
  });

  it("captures the error message instead of swallowing it when boardList rejects", async () => {
    mockBoardList.mockRejectedValue(new Error("HTTP 401: Bad credentials"));

    await service.prefetchAllItems({ force: true });

    expect(service.getLastPrefetchError()).toBe("HTTP 401: Bad credentials");
    // Diagnostics is null because the fetch never reached the diagnostics path.
    expect(service.getLastPrefetchDiagnostics()).toBeNull();
  });

  it("clears prior error + diagnostics at the start of each prefetch", async () => {
    // First call fails — sets lastPrefetchError.
    mockBoardList.mockRejectedValueOnce(new Error("transient"));
    await service.prefetchAllItems({ force: true });
    expect(service.getLastPrefetchError()).toBe("transient");

    // Second call succeeds — error should clear and diagnostics should populate.
    mockBoardList.mockResolvedValueOnce([
      makeBoardItem({ number: 10, repo: "test-org/test-repo" }),
    ]);
    await service.prefetchAllItems({ force: true });

    expect(service.getLastPrefetchError()).toBeNull();
    expect(service.getLastPrefetchDiagnostics()).toEqual({
      rawItemCount: 1,
      filteredItemCount: 1,
      expectedRepo: "test-org/test-repo",
    });
  });

  it("returns null diagnostics when the service is not configured", async () => {
    mockConfigGetProjectConfig.mockResolvedValueOnce({
      owner: null,
      projectNumber: null,
      ownerType: "organization",
    });
    const unconfigured = new ProjectBoardService("/test/workspace");

    await unconfigured.prefetchAllItems({ force: true });

    expect(unconfigured.getLastPrefetchError()).toBeNull();
    expect(unconfigured.getLastPrefetchDiagnostics()).toBeNull();
  });
});
