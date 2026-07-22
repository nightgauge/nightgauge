/**
 * Tests for QueryService
 *
 * Covers:
 * 1. getContext — returns idle state initially
 * 2. validate — calls SDK validate and returns errors
 * 3. execute — transitions state: idle → parsing → executing → complete
 * 4. execute — throws and sets state to error when validation fails
 * 5. execute — fires onQueryComplete on success
 * 6. execute — fires onQueryError and throws when validation fails
 * 7. execute — fires onQueryError when projectBoardService throws
 * 8. execute with status param — calls getIssuesByStatus(status) once
 * 9. execute without status — calls all 4 statuses in parallel
 * 10. reExecute — returns null when no current query
 * 11. reExecute — re-runs current query
 * 12. clear — resets context to idle
 * 13. clearHistory — empties history
 * 14. History is saved after execute (workspaceState.update called)
 * 15. History deduplicates (same query replaces existing entry)
 * 16. History respects maxHistoryEntries (default 20)
 * 17. dispose — disposes EventEmitters without throwing
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryService } from "../../src/services/QueryService";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
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

const mockExecuteQuery = vi.fn();
const mockValidate = vi.fn().mockReturnValue([]);

vi.mock("@nightgauge/sdk", () => ({
  executeQuery: (...args: unknown[]) => mockExecuteQuery(...args),
  validate: (...args: unknown[]) => mockValidate(...args),
}));

// ---------------------------------------------------------------------------
// Factories / helpers
// ---------------------------------------------------------------------------

function makeWorkspaceState() {
  return {
    get: vi.fn().mockReturnValue([]),
    update: vi.fn().mockResolvedValue(undefined),
  };
}

function makeProjectBoardService(issues: object[] = []) {
  return {
    getIssuesByStatus: vi.fn().mockResolvedValue(issues),
  };
}

function makeQueryResult(matchCount = 0) {
  return {
    matchCount,
    issues: [],
    query: "",
    executedAt: new Date(),
  };
}

function makeService(
  opts: {
    issues?: object[];
    workspaceState?: ReturnType<typeof makeWorkspaceState>;
    config?: Record<string, unknown>;
  } = {}
) {
  const workspaceState = opts.workspaceState ?? makeWorkspaceState();
  const projectBoardService = makeProjectBoardService(opts.issues ?? []);
  const service = new QueryService(
    projectBoardService as never,
    workspaceState as never,
    opts.config as never
  );
  return { service, workspaceState, projectBoardService };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("QueryService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockValidate.mockReturnValue([]);
    mockExecuteQuery.mockReturnValue(makeQueryResult(3));
  });

  // -------------------------------------------------------------------------
  // getContext
  // -------------------------------------------------------------------------

  it("getContext — returns idle state initially", () => {
    const { service } = makeService();

    const ctx = service.getContext();

    expect(ctx.state).toBe("idle");
    expect(ctx.query).toBe("");
    expect(ctx.result).toBeUndefined();
    expect(ctx.error).toBeUndefined();
  });

  // -------------------------------------------------------------------------
  // validate
  // -------------------------------------------------------------------------

  it("validate — calls SDK validate and returns errors", () => {
    const errors = [{ message: "Unknown field: foo", column: 0, length: 3 }];
    mockValidate.mockReturnValue(errors);
    const { service } = makeService();

    const result = service.validate("foo:bar");

    expect(mockValidate).toHaveBeenCalledWith("foo:bar");
    expect(result).toEqual(errors);
  });

  // -------------------------------------------------------------------------
  // execute — state transitions
  // -------------------------------------------------------------------------

  it("execute — transitions state through parsing → executing → complete", async () => {
    const states: string[] = [];
    const { service } = makeService();

    service.onQueryStateChanged((ctx) => {
      states.push(ctx.state);
    });

    await service.execute("status:ready");

    expect(states).toContain("parsing");
    expect(states).toContain("executing");
    expect(states).toContain("complete");
    // final state
    expect(service.getContext().state).toBe("complete");
  });

  it("execute — sets state to error when validation fails", async () => {
    mockValidate.mockReturnValue([{ message: "Bad query", column: 0, length: 1 }]);
    const { service } = makeService();

    await expect(service.execute("!!!")).rejects.toThrow("Bad query");
    expect(service.getContext().state).toBe("error");
  });

  // -------------------------------------------------------------------------
  // execute — events
  // -------------------------------------------------------------------------

  it("execute — fires onQueryComplete on success", async () => {
    const result = makeQueryResult(5);
    mockExecuteQuery.mockReturnValue(result);
    const { service } = makeService();

    const completed: unknown[] = [];
    service.onQueryComplete((r) => completed.push(r));

    await service.execute("status:ready");

    expect(completed).toHaveLength(1);
    expect((completed[0] as typeof result).matchCount).toBe(5);
  });

  it("execute — fires onQueryError when validation fails", async () => {
    mockValidate.mockReturnValue([{ message: "Syntax error", column: 0, length: 1 }]);
    const { service } = makeService();

    const errors: string[] = [];
    service.onQueryError((msg) => errors.push(msg));

    await expect(service.execute("bad query")).rejects.toThrow();

    expect(errors).toHaveLength(1);
    expect(errors[0]).toContain("Syntax error");
  });

  it("execute — fires onQueryError when projectBoardService throws", async () => {
    const { service, projectBoardService } = makeService();
    projectBoardService.getIssuesByStatus.mockRejectedValue(new Error("Network error"));

    const errors: string[] = [];
    service.onQueryError((msg) => errors.push(msg));

    await expect(service.execute("status:ready")).rejects.toThrow("Network error");

    expect(errors).toHaveLength(1);
    expect(errors[0]).toContain("Network error");
  });

  // -------------------------------------------------------------------------
  // execute — status param handling
  // -------------------------------------------------------------------------

  it("execute with status — calls getIssuesByStatus once with that status", async () => {
    const { service, projectBoardService } = makeService();

    await service.execute("status:ready", "ready");

    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledTimes(1);
    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledWith("ready");
  });

  it("execute without status — calls all 4 statuses", async () => {
    const { service, projectBoardService } = makeService();

    await service.execute("status:ready");

    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledTimes(4);
    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledWith("ready");
    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledWith("in-progress");
    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledWith("in-review");
    expect(projectBoardService.getIssuesByStatus).toHaveBeenCalledWith("backlog");
  });

  // -------------------------------------------------------------------------
  // reExecute
  // -------------------------------------------------------------------------

  it("reExecute — returns null when no current query", async () => {
    const { service } = makeService();

    const result = await service.reExecute();

    expect(result).toBeNull();
  });

  it("reExecute — re-runs the current query", async () => {
    const { service } = makeService();

    await service.execute("priority:P0");
    const result = await service.reExecute();

    expect(result).not.toBeNull();
    expect(service.getCurrentQuery()).toBe("priority:P0");
  });

  // -------------------------------------------------------------------------
  // clear / clearHistory
  // -------------------------------------------------------------------------

  it("clear — resets context to idle", async () => {
    const { service } = makeService();

    await service.execute("status:ready");
    expect(service.getContext().state).toBe("complete");

    service.clear();

    const ctx = service.getContext();
    expect(ctx.state).toBe("idle");
    expect(ctx.query).toBe("");
    expect(ctx.result).toBeUndefined();
    expect(ctx.error).toBeUndefined();
  });

  it("clearHistory — empties history", async () => {
    const { service } = makeService();

    await service.execute("status:ready");
    expect(service.getHistory()).toHaveLength(1);

    service.clearHistory();

    expect(service.getHistory()).toHaveLength(0);
  });

  // -------------------------------------------------------------------------
  // History persistence
  // -------------------------------------------------------------------------

  it("history is saved after execute (workspaceState.update called)", async () => {
    const { service, workspaceState } = makeService();

    await service.execute("status:ready");

    expect(workspaceState.update).toHaveBeenCalledWith(
      "nightgauge.queryHistory",
      expect.arrayContaining([expect.objectContaining({ query: "status:ready" })])
    );
  });

  it("history deduplicates — same query replaces existing entry", async () => {
    const { service } = makeService();

    await service.execute("status:ready");
    await service.execute("priority:P0");
    await service.execute("status:ready"); // duplicate

    const history = service.getHistory();
    const readyEntries = history.filter((h) => h.query === "status:ready");
    expect(readyEntries).toHaveLength(1);
    // most recent execute should be at front
    expect(history[0].query).toBe("status:ready");
  });

  it("history respects maxHistoryEntries limit", async () => {
    const { service } = makeService({ config: { maxHistoryEntries: 3 } });

    for (let i = 0; i < 5; i++) {
      await service.execute(`query-${i}`);
    }

    expect(service.getHistory()).toHaveLength(3);
  });

  // -------------------------------------------------------------------------
  // dispose
  // -------------------------------------------------------------------------

  it("dispose — disposes EventEmitters without throwing", () => {
    const { service } = makeService();

    expect(() => service.dispose()).not.toThrow();
  });
});
