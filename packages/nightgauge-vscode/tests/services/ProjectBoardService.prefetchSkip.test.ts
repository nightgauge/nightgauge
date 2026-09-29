/**
 * A board prefetch that runs before the project config resolves reads no
 * board at all, and must say so rather than look like an empty board (#2287).
 *
 * The race it pins: the dashboard's first board refresh starts a config
 * request, a workspace-root change supersedes that request, and the prefetch
 * returns with no owner or project. It is then the config-resolved event that
 * lets the reader try again.
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  window: { showWarningMessage: vi.fn() },
  workspace: { getConfiguration: () => ({ get: () => undefined }) },
  EventEmitter: class {
    private listeners: Array<() => void> = [];
    event = (listener: () => void) => {
      this.listeners.push(listener);
      return { dispose: () => (this.listeners = this.listeners.filter((l) => l !== listener)) };
    };
    fire = () => this.listeners.forEach((l) => l());
    dispose = () => (this.listeners = []);
  },
}));
vi.mock("../../src/utils/repoInitialized", () => ({ isRepoInitialized: async () => false }));

import { ProjectBoardService } from "../../src/services/ProjectBoardService";

const ROOT = "/test/workspace";

interface ConfigReply {
  owner?: string;
  projectNumber?: number;
}

/** A service whose config requests wait until the test answers them. */
function service() {
  const pending: Array<(reply: ConfigReply) => void> = [];
  const fetched: string[] = [];
  const snapshots = {
    fetch: vi.fn(async (key: { owner: string }) => {
      fetched.push(key.owner);
      return [];
    }),
    invalidateBoard: vi.fn(),
    expireBoard: vi.fn(),
    getMetrics: vi.fn(),
  };
  const svc = new ProjectBoardService(ROOT, undefined, snapshots as never);
  (svc as unknown as { ipc: unknown }).ipc = {
    configGetProjectConfig: vi.fn(
      () =>
        new Promise<ConfigReply & { projects: [] }>((r) =>
          pending.push((c) => r({ ...c, projects: [] }))
        )
    ),
  };
  const resolved = vi.fn();
  svc.onDidResolveConfig(resolved);
  const answer = async (reply: ConfigReply) => {
    pending.shift()!(reply);
    await new Promise((r) => setImmediate(r));
  };
  return { svc, answer, fetched, resolved };
}

describe("ProjectBoardService prefetch before the config resolves (#2287)", () => {
  it("reports a superseded config as unresolved, then reads the board once it resolves", async () => {
    const { svc, answer, fetched, resolved } = service();

    // The dashboard's early refresh: its config request is in flight...
    const early = svc.prefetchAllItems({ force: true });
    await Promise.resolve();
    // ...when the workspace root is set, which supersedes it.
    svc.updateWorkspaceRoot(ROOT);
    await answer({ owner: "acme", projectNumber: 7 });
    await early;

    expect(svc.getLastPrefetchSkip()).toBe("config-unresolved");
    expect(fetched).toEqual([]);
    expect(resolved).not.toHaveBeenCalled();

    // Another reader (a tree view) loads the config for the new root.
    const load = svc.loadConfig();
    await answer({ owner: "acme", projectNumber: 7 });
    await load;
    expect(resolved).toHaveBeenCalledTimes(1);

    await svc.prefetchAllItems({ force: true });
    expect(svc.getLastPrefetchSkip()).toBeNull();
    expect(fetched).toEqual(["acme"]);
  });

  it("reports a loaded config with no project as not configured, and fires nothing", async () => {
    const { svc, answer, resolved } = service();
    const prefetch = svc.prefetchAllItems();
    await Promise.resolve();
    await answer({ owner: "acme" });
    await prefetch;

    expect(svc.getLastPrefetchSkip()).toBe("not-configured");
    expect(resolved).not.toHaveBeenCalled();
  });
});
