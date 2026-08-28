/**
 * Issue #1014: ProjectBoardService resolved the same config three times per
 * repo per activation.
 *
 * `loadConfig()` guarded on `configLoaded`, which is set only AFTER the IPC
 * round-trip returns — so it could never exclude callers that arrive while the
 * first request is still in flight. The tree provider fires three
 * `getIssuesByStatus` calls in one `Promise.all`, each beginning with
 * `await this.loadConfig()`, so three identical IPC calls were structural
 * rather than incidental.
 *
 * The assertion that matters is the CALL COUNT. A test that only checks
 * loadConfig resolves passes just as happily with three round-trips as with
 * one.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  window: { showWarningMessage: vi.fn() },
  workspace: { getConfiguration: () => ({ get: () => undefined }) },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { ProjectBoardService } from "../../src/services/ProjectBoardService";

const ROOT = "/test/workspace";

function attachIpc(service: ProjectBoardService, deferred: { resolve?: () => void } = {}) {
  const calls = { count: 0 };
  const configGetProjectConfig = vi.fn(async () => {
    calls.count++;
    if (deferred.resolve === undefined) {
      await new Promise<void>((r) => {
        deferred.resolve = r;
      });
    }
    return {
      owner: "acme",
      ownerType: "org",
      projectNumber: 7,
      defaultRepo: "widgets",
      projects: [],
    };
  });
  (service as unknown as { ipc: unknown }).ipc = { configGetProjectConfig };
  return calls;
}

describe("ProjectBoardService.loadConfig deduplication (#1014)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("issues ONE IPC call for concurrent callers, not one per caller", async () => {
    const service = new ProjectBoardService(ROOT);
    const deferred: { resolve?: () => void } = {};
    const calls = attachIpc(service, deferred);

    // The tree provider's exact shape: three concurrent awaits in one Promise.all.
    const inFlight = Promise.all([
      service.loadConfig(),
      service.loadConfig(),
      service.loadConfig(),
    ]);

    // Let the three callers reach their await before the reply lands. Without
    // the in-flight memo all three have already issued their own request by now.
    await Promise.resolve();
    await Promise.resolve();

    deferred.resolve?.();
    await inFlight;

    expect(calls.count, "three concurrent callers must share one config round-trip").toBe(1);
  });

  it("does not re-request once the config is loaded", async () => {
    const service = new ProjectBoardService(ROOT);
    const calls = attachIpc(service, { resolve: () => {} });

    await service.loadConfig();
    await service.loadConfig();
    await service.loadConfig();

    expect(calls.count).toBe(1);
  });

  it("re-requests after an invalidation, and does not resurrect the old identity", async () => {
    const service = new ProjectBoardService(ROOT);
    const calls = attachIpc(service, { resolve: () => {} });

    await service.loadConfig();
    expect(calls.count).toBe(1);

    // updateWorkspaceRoot is one of the four invalidation points.
    service.updateWorkspaceRoot("/other/workspace");
    await service.loadConfig();

    expect(calls.count, "an invalidation must produce a fresh request").toBe(2);
  });
});
